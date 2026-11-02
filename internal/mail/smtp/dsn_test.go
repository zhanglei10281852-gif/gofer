package smtp

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

type fakeDSNSMTP struct {
	advertiseDSN bool
	lines        []string
}

func runFakeDSNSMTP(t *testing.T, f *fakeDSNSMTP) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		write := func(text string) { _, _ = conn.Write([]byte(text)) }
		write("220 fake ESMTP ready\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.TrimSpace(line)
			upper := strings.ToUpper(command)
			f.lines = append(f.lines, command)
			switch {
			case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
				write("250-fake.example at your service\r\n")
				write("250-AUTH PLAIN\r\n")
				if f.advertiseDSN {
					write("250-DSN\r\n")
				}
				write("250 SIZE 10485760\r\n")
			case strings.HasPrefix(upper, "AUTH"):
				// go-smtp sends the PLAIN initial response with the command.
				write("235 authenticated\r\n")
			case strings.HasPrefix(upper, "MAIL") || strings.HasPrefix(upper, "RCPT"):
				write("250 ok\r\n")
			case strings.HasPrefix(upper, "DATA"):
				write("354 send content\r\n")
				for {
					body, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(body) == "." {
						break
					}
				}
				write("250 queued\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				write("221 bye\r\n")
				return
			default:
				write("250 ok\r\n")
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return host, port
}

func dsnTestConfig(host string, port int) *models.AccountConfig {
	return &models.AccountConfig{
		SMTPHost: host, SMTPPort: port,
		SMTPTLSMode:        "plaintext",
		SMTPAllowPlaintext: true,
		SMTPAllowPrivate:   true,
		Username:           "user@example.com",
		AuthMethod:         "plain",
	}
}

func joinCommand(lines []string, prefix string) string {
	for _, line := range lines {
		if strings.HasPrefix(strings.ToUpper(line), prefix) {
			return strings.ToUpper(line)
		}
	}
	return ""
}

func TestDeliverRawRequestsDSNWhenAdvertised(t *testing.T) {
	fake := &fakeDSNSMTP{advertiseDSN: true}
	host, port := runFakeDSNSMTP(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := NewClient(ctx, dsnTestConfig(host, port), "secret")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer c.Close()
	outcome, err := c.DeliverRaw(ctx, "sender@example.com", []string{"one@example.org", "two@example.net"},
		[]byte("Subject: hi\r\n\r\nhello"), &DSNRequest{EnvelopeID: "g0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("DeliverRaw() error = %v", err)
	}
	if outcome.Result != models.SendSuccess || !outcome.DSNRequested || !outcome.DSNAvailable {
		t.Fatalf("outcome = %#v, want success with DSN negotiated", outcome)
	}
	mail := joinCommand(fake.lines, "MAIL")
	if !strings.Contains(mail, "ENVID=G0123456789ABCDEF0123456789ABCDEF") || !strings.Contains(mail, "RET=HDRS") {
		t.Fatalf("MAIL command = %q, want ENVID and RET", mail)
	}
	rcpts := 0
	for _, line := range fake.lines {
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "RCPT") {
			continue
		}
		rcpts++
		if !strings.Contains(upper, "NOTIFY=SUCCESS,FAILURE,DELAY") || !strings.Contains(upper, "ORCPT=RFC822;") {
			t.Fatalf("RCPT command = %q, want NOTIFY and ORCPT", line)
		}
	}
	if rcpts != 2 {
		t.Fatalf("RCPT count = %d, want 2", rcpts)
	}
}

func TestDeliverRawFallsBackWhenDSNNotAdvertised(t *testing.T) {
	fake := &fakeDSNSMTP{advertiseDSN: false}
	host, port := runFakeDSNSMTP(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := NewClient(ctx, dsnTestConfig(host, port), "secret")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer c.Close()
	outcome, err := c.DeliverRaw(ctx, "sender@example.com", []string{"one@example.org"},
		[]byte("Subject: hi\r\n\r\nhello"), &DSNRequest{EnvelopeID: "g0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("DeliverRaw() error = %v", err)
	}
	if outcome.Result != models.SendSuccess || !outcome.DSNRequested || outcome.DSNAvailable {
		t.Fatalf("outcome = %#v, want ordinary success with DSN requested but unavailable", outcome)
	}
	for _, line := range fake.lines {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "ENVID=") || strings.Contains(upper, "NOTIFY=") {
			t.Fatalf("DSN parameters sent to non-DSN server: %q", line)
		}
	}
}

func TestDeliverRawWithoutRequestSendsPlain(t *testing.T) {
	fake := &fakeDSNSMTP{advertiseDSN: true}
	host, port := runFakeDSNSMTP(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := NewClient(ctx, dsnTestConfig(host, port), "secret")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer c.Close()
	outcome, err := c.DeliverRaw(ctx, "sender@example.com", []string{"one@example.org"},
		[]byte("Subject: hi\r\n\r\nhello"), nil)
	if err != nil || outcome.Result != models.SendSuccess || outcome.DSNRequested {
		t.Fatalf("outcome = %#v, err = %v, want plain success", outcome, err)
	}
	for _, line := range fake.lines {
		if strings.Contains(strings.ToUpper(line), "NOTIFY=") {
			t.Fatalf("NOTIFY sent without request: %q", line)
		}
	}
}
