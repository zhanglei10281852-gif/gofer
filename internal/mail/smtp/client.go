package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/mail/oauth2sasl"
	mailtransport "github.com/cristianadrielbraun/gofer/internal/mail/transport"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/netguard"
)

type Client struct {
	config *models.AccountConfig
	conn   net.Conn
	client *smtp.Client
}

// DeliveryTiming contains the durations and connection usage for one SMTP
// delivery attempt. It is intentionally separate from the delivery result so
// callers can measure the existing one-message-per-connection behavior before
// deciding whether connection reuse is worth the added state machine.
type DeliveryTiming struct {
	ConnectAuth           time.Duration
	Data                  time.Duration
	Total                 time.Duration
	QueueWait             time.Duration
	ConnectionEstablished bool
	MessagesPerConnection int
}

type deliveryError struct {
	err       error
	retryable bool
}

func (e *deliveryError) Error() string { return e.err.Error() }
func (e *deliveryError) Unwrap() error { return e.err }

func markDeliveryError(err error, retryable bool) error {
	if err == nil {
		return nil
	}
	var deliveryErr *deliveryError
	if errors.As(err, &deliveryErr) {
		return err
	}
	return &deliveryError{err: err, retryable: retryable}
}

func classifyPreAcceptanceError(err error) error {
	var smtpErr *smtp.SMTPError
	if errors.As(err, &smtpErr) {
		return markDeliveryError(err, smtpErr.Code/100 == 4)
	}
	return markDeliveryError(err, true)
}

func IsRetryable(err error) bool {
	var deliveryErr *deliveryError
	return errors.As(err, &deliveryErr) && deliveryErr.retryable
}

const (
	connectTimeout         = 15 * time.Second
	setupTimeout           = 30 * time.Second
	commandTimeout         = 30 * time.Second
	submissionTimeout      = 5 * time.Minute
	outgoingMessageTimeout = 5 * time.Minute
)

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func deadlineForContext(ctx context.Context, fallback time.Duration) time.Time {
	deadline := time.Now().Add(fallback)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

func configureTimeouts(client *smtp.Client) {
	client.CommandTimeout = commandTimeout
	client.SubmissionTimeout = submissionTimeout
}

func NewClient(ctx context.Context, cfg *models.AccountConfig, password string) (*Client, error) {
	tlsMode, err := mailtransport.RequireTLSModeWithPlaintext("SMTP", cfg.SMTPTLSMode, cfg.SMTPAllowPlaintext)
	if err != nil {
		return nil, markDeliveryError(err, false)
	}
	if tlsMode == mailtransport.TLSModePlaintext && !strings.EqualFold(strings.TrimSpace(cfg.AuthMethod), "plain") {
		return nil, markDeliveryError(fmt.Errorf("SMTP OAuth authentication is not allowed over a plaintext connection"), false)
	}
	ctx = nonNilContext(ctx)
	setupCtx, cancelSetup := context.WithTimeout(ctx, setupTimeout)
	defer cancelSetup()
	if err := setupCtx.Err(); err != nil {
		return nil, markDeliveryError(err, true)
	}

	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	dialer := &net.Dialer{Timeout: connectTimeout}

	var conn net.Conn
	var client *smtp.Client

	switch tlsMode {
	case mailtransport.TLSModeImplicit:
		conn, err = dialSMTP(setupCtx, dialer, cfg, addr)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: cfg.SMTPHost,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(setupCtx); err != nil {
			_ = conn.Close()
			return nil, markDeliveryError(fmt.Errorf("connect to %s: %w", addr, contextError(setupCtx, err)), true)
		}
		conn = tlsConn
		client = smtp.NewClient(conn)

	case mailtransport.TLSModeStartTLS:
		conn, err = dialSMTP(setupCtx, dialer, cfg, addr)
		if err != nil {
			return nil, err
		}
		stopCancellation := context.AfterFunc(setupCtx, func() { _ = conn.Close() })
		defer stopCancellation()
		tlsConfig := &tls.Config{
			ServerName: cfg.SMTPHost,
			MinVersion: tls.VersionTLS12,
		}
		client, err = smtp.NewClientStartTLS(conn, tlsConfig)
		if err != nil {
			_ = conn.Close()
			return nil, classifyPreAcceptanceError(fmt.Errorf("starttls: %w", contextError(setupCtx, err)))
		}

	case mailtransport.TLSModePlaintext:
		conn, err = dialSMTP(setupCtx, dialer, cfg, addr)
		if err != nil {
			return nil, err
		}
		client = smtp.NewClient(conn)

	default:
		return nil, markDeliveryError(fmt.Errorf("unsupported SMTP TLS mode %q", tlsMode), false)
	}
	stopCancellation := context.AfterFunc(setupCtx, func() { _ = conn.Close() })
	defer stopCancellation()
	configureTimeouts(client)

	if err := client.Hello("localhost"); err != nil {
		_ = conn.Close()
		return nil, classifyPreAcceptanceError(fmt.Errorf("ehlo: %w", contextError(setupCtx, err)))
	}

	smtpUsername := cfg.SmtpUsername
	if smtpUsername == "" {
		smtpUsername = cfg.Username
	}

	switch cfg.AuthMethod {
	case "plain":
		saslClient := sasl.NewPlainClient("", smtpUsername, password)
		err = client.Auth(saslClient)
	case "oauth2":
		saslClient := oauth2sasl.NewClient(smtpUsername, password)
		err = client.Auth(saslClient)
	default:
		saslClient := sasl.NewPlainClient("", smtpUsername, password)
		err = client.Auth(saslClient)
	}

	if err != nil {
		_ = conn.Close()
		return nil, classifyPreAcceptanceError(fmt.Errorf("authenticate: %w", contextError(setupCtx, err)))
	}

	return &Client{
		config: cfg,
		conn:   conn,
		client: client,
	}, nil
}

// dialSMTP opens the connection through netguard. A refused private server
// won't start working on a retry, so that failure is final.
func dialSMTP(ctx context.Context, dialer *net.Dialer, cfg *models.AccountConfig, addr string) (net.Conn, error) {
	conn, err := netguard.Dial(ctx, dialer, "smtp", cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPAllowPrivate)
	if err != nil {
		return nil, markDeliveryError(fmt.Errorf("connect to %s: %w", addr, contextError(ctx, err)), !netguard.IsBlocked(err))
	}
	return conn, nil
}

// DSNRequest asks an SMTP server for Delivery Status Notifications bound to
// one envelope. EnvelopeID is the ENVID the later reports must answer for.
type DSNRequest struct {
	EnvelopeID string
}

// DeliveryOutcome keeps the server's acceptance result separate from whether
// delivery reports were actually requested: a DSN-capable server may accept
// the message, and the absence of later reports never means delivered or
// failed.
type DeliveryOutcome struct {
	Result models.SendResult
	// DSNRequested is set when the caller asked for delivery notifications.
	DSNRequested bool
	// DSNAvailable is set only when the accepting server advertised DSN and
	// the ENVID and per-recipient NOTIFY parameters were transmitted.
	DSNAvailable bool
}

func (c *Client) Send(ctx context.Context, msg *message.OutgoingMessage) (models.SendResult, error) {
	recipients := message.AllRecipients(msg)
	if len(recipients) == 0 {
		return models.SendFailed, markDeliveryError(fmt.Errorf("no recipients"), false)
	}
	mimeData, err := message.BuildMIMEMessage(msg)
	if err != nil {
		return models.SendFailed, markDeliveryError(fmt.Errorf("build mime: %w", err), false)
	}
	return c.SendRaw(ctx, msg.FromEmail, recipients, mimeData)
}

func (c *Client) SendRaw(ctx context.Context, from string, recipients []string, mimeData []byte) (models.SendResult, error) {
	outcome, err := c.DeliverRaw(ctx, from, recipients, mimeData, nil)
	return outcome.Result, err
}

// DeliverRaw sends one message, optionally binding an ENVID and per-recipient
// NOTIFY requests when the server advertises DSN. When it does not, the
// message is still sent as ordinary mail; the outcome says reports could not
// be requested so callers can show that instead of guessing delivery.
func (c *Client) DeliverRaw(ctx context.Context, from string, recipients []string, mimeData []byte, request *DSNRequest) (DeliveryOutcome, error) {
	outcome := DeliveryOutcome{Result: models.SendFailed}
	if len(recipients) == 0 {
		return outcome, markDeliveryError(fmt.Errorf("no recipients"), false)
	}
	ctx = nonNilContext(ctx)
	sendCtx, cancelSend := context.WithTimeout(ctx, outgoingMessageTimeout)
	defer cancelSend()
	if err := sendCtx.Err(); err != nil {
		return outcome, markDeliveryError(err, true)
	}
	stopCancellation := context.AfterFunc(sendCtx, func() { _ = c.conn.Close() })
	defer stopCancellation()

	messageSize := int64(len(mimeData))
	if maxSize, ok := c.client.MaxMessageSize(); ok && maxSize > 0 && messageSize > int64(maxSize) {
		return outcome, markDeliveryError(fmt.Errorf("message is %d bytes, but the SMTP server accepts at most %d bytes", messageSize, maxSize), false)
	}

	requiresSMTPUTF8 := smtpEnvelopeRequiresUTF8(from, recipients)
	mailOptions := &smtp.MailOptions{
		Size: messageSize,
		UTF8: requiresSMTPUTF8,
	}
	if request != nil && strings.TrimSpace(request.EnvelopeID) != "" {
		outcome.DSNRequested = true
		if ok, _ := c.client.Extension("DSN"); ok {
			outcome.DSNAvailable = true
			mailOptions.Return = smtp.DSNReturnHeaders
			mailOptions.EnvelopeID = strings.TrimSpace(request.EnvelopeID)
		}
	}
	if err := c.client.Mail(from, mailOptions); err != nil {
		mailErr := fmt.Errorf("mail from: %w", contextError(sendCtx, err))
		if requiresSMTPUTF8 && strings.Contains(strings.ToLower(mailErr.Error()), "server does not support smtputf8") {
			return outcome, markDeliveryError(fmt.Errorf("SMTPUTF8 is required for %s, but the server does not advertise SMTPUTF8", smtpUTF8RequirementDescription(from, recipients)), false)
		}
		return outcome, classifyPreAcceptanceError(mailErr)
	}

	for _, rcpt := range recipients {
		if err := c.client.Rcpt(rcpt, smtpRcptOptions(outcome.DSNAvailable, rcpt)); err != nil {
			return outcome, classifyPreAcceptanceError(fmt.Errorf("rcpt to %s: %w", rcpt, contextError(sendCtx, err)))
		}
	}

	dataw, err := c.client.Data()
	if err != nil {
		return outcome, classifyPreAcceptanceError(fmt.Errorf("data: %w", contextError(sendCtx, err)))
	}
	if err := c.conn.SetDeadline(deadlineForContext(sendCtx, submissionTimeout)); err != nil {
		_ = c.conn.Close()
		return outcome, markDeliveryError(fmt.Errorf("set data deadline: %w", contextError(sendCtx, err)), true)
	}

	if _, err := dataw.Write(mimeData); err != nil {
		_ = c.conn.Close()
		outcome.Result = models.SendAmbiguous
		return outcome, fmt.Errorf("write data: %w", contextError(sendCtx, err))
	}

	if err := dataw.Close(); err != nil {
		var smtpErr *smtp.SMTPError
		if errors.As(err, &smtpErr) {
			return outcome, classifyPreAcceptanceError(fmt.Errorf("close data: %w", contextError(sendCtx, err)))
		}
		outcome.Result = models.SendAmbiguous
		return outcome, fmt.Errorf("close data: %w", contextError(sendCtx, err))
	}

	outcome.Result = models.SendSuccess
	return outcome, nil
}

// smtpRcptOptions builds per-recipient DSN options. NOTIFY asks for every
// outcome; ORCPT names the original address for ASCII mailboxes so reports
// can be matched after forwarding, but is omitted where encoding could fail.
func smtpRcptOptions(dsnAvailable bool, rcpt string) *smtp.RcptOptions {
	if !dsnAvailable {
		return nil
	}
	options := &smtp.RcptOptions{
		Notify: []smtp.DSNNotify{smtp.DSNNotifySuccess, smtp.DSNNotifyFailure, smtp.DSNNotifyDelayed},
	}
	if !smtpAddressRequiresUTF8(rcpt) {
		options.OriginalRecipientType = smtp.DSNAddressTypeRFC822
		options.OriginalRecipient = rcpt
	}
	return options
}

func (c *Client) sendRawWithTiming(ctx context.Context, from string, recipients []string, mimeData []byte) (models.SendResult, error, DeliveryTiming) {
	startedAt := time.Now()
	outcome, err := c.DeliverRaw(ctx, from, recipients, mimeData, nil)
	return outcome.Result, err, DeliveryTiming{
		Data:                  time.Since(startedAt),
		Total:                 time.Since(startedAt),
		ConnectionEstablished: true,
		MessagesPerConnection: 1,
	}
}

func smtpEnvelopeRequiresUTF8(from string, recipients []string) bool {
	if smtpAddressRequiresUTF8(from) {
		return true
	}
	for _, recipient := range recipients {
		if smtpAddressRequiresUTF8(recipient) {
			return true
		}
	}
	return false
}

func smtpUTF8RequirementDescription(from string, recipients []string) string {
	if smtpAddressRequiresUTF8(from) {
		return "the sender address"
	}
	return "a recipient address"
}

func smtpAddressRequiresUTF8(address string) bool {
	for _, r := range address {
		if r > 127 {
			return true
		}
	}
	return false
}

func (c *Client) Close() error {
	return c.client.Close()
}

func TestConnection(ctx context.Context, cfg *models.AccountConfig, password string) error {
	c, err := NewClient(ctx, cfg, password)
	if err != nil {
		return err
	}
	return c.Close()
}

func SendMessage(ctx context.Context, cfg *models.AccountConfig, password string, msg *message.OutgoingMessage) (models.SendResult, error) {
	c, err := NewClient(ctx, cfg, password)
	if err != nil {
		return models.SendFailed, err
	}
	defer c.Close()

	return c.Send(ctx, msg)
}

func SendRawMessage(ctx context.Context, cfg *models.AccountConfig, password, from string, recipients []string, mimeData []byte) (models.SendResult, error) {
	result, err, _ := SendRawMessageWithTiming(ctx, cfg, password, from, recipients, mimeData)
	return result, err
}

// SendRawMessageWithDelivery sends one message with a fresh SMTP connection,
// optionally requesting DSN, and returns what the accepting server granted.
func SendRawMessageWithDelivery(ctx context.Context, cfg *models.AccountConfig, password, from string, recipients []string, mimeData []byte, request *DSNRequest) (DeliveryOutcome, error, DeliveryTiming) {
	startedAt := time.Now()
	c, err := NewClient(ctx, cfg, password)
	timing := DeliveryTiming{ConnectAuth: time.Since(startedAt)}
	outcome := DeliveryOutcome{Result: models.SendFailed, DSNRequested: request != nil}
	if err != nil {
		timing.Total = time.Since(startedAt)
		return outcome, err, timing
	}
	defer c.Close()

	timing.ConnectionEstablished = true
	timing.MessagesPerConnection = 1
	outcome, err = c.DeliverRaw(ctx, from, recipients, mimeData, request)
	timing.Data = time.Since(startedAt)
	timing.Total = time.Since(startedAt)
	return outcome, err, timing
}

// SendRawMessageWithTiming sends one message with a fresh SMTP connection and
// returns timing data for profiling. It preserves the existing connection
// lifetime and delivery classification; the timing is observational only.
func SendRawMessageWithTiming(ctx context.Context, cfg *models.AccountConfig, password, from string, recipients []string, mimeData []byte) (models.SendResult, error, DeliveryTiming) {
	outcome, err, timing := SendRawMessageWithDelivery(ctx, cfg, password, from, recipients, mimeData, nil)
	return outcome.Result, err, timing
}
