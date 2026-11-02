// Package dsn parses standard Delivery Status Notifications (RFC 3461/3464)
// and builds the SMTP DSN parameters a sender requests for an outgoing
// message. It never sends, rewrites, or stores mail itself.
package dsn

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-message"
)

// RecipientRecord is one per-recipient record of a delivery-status part.
type RecipientRecord struct {
	Action            string
	FinalRecipient    string
	OriginalRecipient string
	StatusCode        string
	Diagnostic        string
	LastAttempt       time.Time
}

// Report is the provider-neutral content of a multipart/report DSN.
type Report struct {
	// MessageID is the report email's own Message-ID header.
	MessageID string
	// EnvelopeID is the Original-Envelope-Id the report answers for.
	EnvelopeID string
	Arrival    time.Time
	Recipients []RecipientRecord
}

// ParseReport reads a full message and returns its DSN content when it is a
// standard multipart/report; report-type=delivery-status message. The boolean
// is false for anything else, including other report types and ordinary mail.
func ParseReport(raw []byte) (*Report, bool, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return nil, false, nil
	}
	contentType, params, err := entity.Header.ContentType()
	if err != nil || !strings.EqualFold(contentType, "multipart/report") {
		return nil, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(params["report-type"]), "delivery-status") {
		return nil, false, nil
	}
	report := &Report{}
	if id, err := entity.Header.Text("Message-Id"); err == nil {
		report.MessageID = strings.TrimSpace(id)
	}

	mr := entity.MultipartReader()
	if mr == nil {
		return nil, false, nil
	}
	statusParsed := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if statusParsed {
				// The human-readable tail may be truncated without invalidating
				// the machine records already extracted.
				break
			}
			return nil, false, nil
		}
		partType, _, _ := part.Header.ContentType()
		partType = strings.ToLower(strings.TrimSpace(partType))
		body, readErr := io.ReadAll(part.Body)
		if readErr != nil && !statusParsed {
			return nil, false, nil
		}
		switch partType {
		case "message/delivery-status", "message/global-delivery-status":
			parseDeliveryStatus(body, report)
			statusParsed = true
		}
	}
	if !statusParsed {
		return nil, false, nil
	}
	return report, true, nil
}

// parseDeliveryStatus parses one or more RFC 3464 field blocks: exactly one
// per-message block followed by zero or more per-recipient blocks.
func parseDeliveryStatus(raw []byte, report *Report) {
	blocks := splitStatusBlocks(raw)
	for _, block := range blocks {
		header, err := readStatusHeader(block)
		if err != nil {
			// A bad block must not prevent later blocks from being used.
			continue
		}
		// RFC 3464: per-message blocks carry Original-Envelope-Id and never
		// Action; per-recipient blocks carry Action. Classify by content so a
		// dropped empty block cannot shift a result into the wrong kind.
		if strings.TrimSpace(header.Get("Action")) != "" {
			record := recipientRecordFromHeader(header)
			if record.Action != "" {
				report.Recipients = append(report.Recipients, record)
			}
			continue
		}
		if envID := DecodeXText(strings.TrimSpace(header.Get("Original-Envelope-Id"))); envID != "" {
			report.EnvelopeID = envID
		}
		if arrival := parseStatusTime(header.Get("Arrival-Date")); !arrival.IsZero() {
			report.Arrival = arrival
		}
	}
}

func recipientRecordFromHeader(header textproto.MIMEHeader) RecipientRecord {
	record := RecipientRecord{
		Action:     normalizeAction(header.Get("Action")),
		StatusCode: strings.TrimSpace(header.Get("Status")),
		Diagnostic: strings.TrimSpace(stripDiagnosticType(header.Get("Diagnostic-Code"))),
	}
	if t := parseStatusTime(header.Get("Last-Attempt-Date")); !t.IsZero() {
		record.LastAttempt = t
	}
	if addr := typedAddress(header.Get("Final-Recipient")); addr != "" {
		record.FinalRecipient = addr
	}
	if addr := typedAddress(header.Get("Original-Recipient")); addr != "" {
		record.OriginalRecipient = addr
	}
	return record
}

// splitStatusBlocks separates RFC 3464 blocks by blank lines, tolerating CRLF
// or LF and folded continuation lines.
func splitStatusBlocks(raw []byte) [][]byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// Unfold continuation lines so blank-line splitting stays block based.
	text = strings.ReplaceAll(text, "\n\t", " ")
	text = strings.ReplaceAll(text, "\n ", " ")
	rawBlocks := strings.Split(text, "\n\n")
	blocks := make([][]byte, 0, len(rawBlocks))
	for _, block := range rawBlocks {
		block = strings.TrimSpace(block)
		if block != "" {
			blocks = append(blocks, []byte(strings.ReplaceAll(block, "\n", "\r\n")))
		}
	}
	return blocks
}

func readStatusHeader(block []byte) (textproto.MIMEHeader, error) {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(block)))
	header, err := reader.ReadMIMEHeader()
	if err != nil && len(header) == 0 {
		return nil, err
	}
	return header, nil
}

// typedAddress parses "type; value" fields such as Final-Recipient and
// xtext-decodes the value. Real servers sometimes leave plus signs in local
// parts unencoded, so decoding only happens for an encoded-looking value.
func typedAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.Index(value, ";"); idx >= 0 {
		value = value[idx+1:]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "<>")
	if looksXTextEncoded(value) {
		if decoded := DecodeXText(value); decoded != "" {
			value = decoded
		}
	}
	return strings.TrimSpace(value)
}

func looksXTextEncoded(value string) bool {
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '+' {
			if isHex(value[i+1]) && isHex(value[i+2]) {
				return true
			}
		}
	}
	return false
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'F') || (b >= 'a' && b <= 'f')
}

func normalizeAction(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fields[0]))
}

func stripDiagnosticType(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, ";"); idx >= 0 {
		return strings.TrimSpace(value[idx+1:])
	}
	return value
}

func parseStatusTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.ANSIC} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// DecodeXText decodes the xtext encoding RFC 3461 uses in ENVID and typed
// addresses. Malformed sequences are returned verbatim rather than rejected, so
// a quirky MTA cannot suppress the whole report.
func DecodeXText(value string) string {
	if !strings.Contains(value, "+") {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '+' && i+2 < len(value) && isHex(value[i+1]) && isHex(value[i+2]) {
			hi, _ := hex.DecodeString(value[i+1 : i+3])
			builder.WriteByte(hi[0])
			i += 2
			continue
		}
		builder.WriteByte(value[i])
	}
	return builder.String()
}

// ReportKey returns the stable identity of a logical report. Its Message-ID is
// preferred; otherwise the content that matters for correlation is hashed, so
// reopening or receiving the same bytes twice never counts it twice.
func ReportKey(report *Report) string {
	if report == nil {
		return ""
	}
	if id := strings.TrimSpace(report.MessageID); id != "" {
		return "mid:" + strings.Trim(id, "<>")
	}
	var canonical bytes.Buffer
	fmt.Fprintf(&canonical, "env=%s", strings.ToLower(report.EnvelopeID))
	for _, recipient := range report.Recipients {
		fmt.Fprintf(&canonical, "|%s;%s;%s", strings.ToLower(recipient.FinalRecipient), recipient.Action, recipient.StatusCode)
	}
	if !report.Arrival.IsZero() {
		fmt.Fprintf(&canonical, "|%d", report.Arrival.Unix())
	}
	sum := sha256.Sum256(canonical.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NewEnvelopeID builds the xtext-safe ENVID (RFC 3461) that binds one send to
// its later reports. It stays the same across retries of that send.
func NewEnvelopeID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is not recoverable in practice; fall back to a
		// timestamp so a send still gets a syntactically valid identifier.
		return fmt.Sprintf("gofer%d", time.Now().UnixNano())
	}
	return "g" + hex.EncodeToString(b[:])
}
