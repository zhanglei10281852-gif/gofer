package dsn

import (
	"strings"
	"testing"
)

func TestParseReportDeliveredFailureAndDelay(t *testing.T) {
	raw := []byte(strings.ReplaceAll(`From: Mail Delivery System <mailer-daemon@mx.example.com>
To: <sender@example.com>
Subject: Delivery Status Notification
Message-ID: <dsn-1@mx.example.com>
Date: Mon, 02 Nov 2026 10:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/report; report-type=delivery-status;
	boundary="BOUND"

--BOUND
Content-Type: text/plain

Delivery report.

--BOUND
Content-Type: message/delivery-status

Reporting-MTA: dns; mx.example.com
Original-Envelope-Id: gABCDEF0123456789abcdef0123456789
Arrival-Date: Mon, 02 Nov 2026 09:00:00 +0000

Final-Recipient: rfc822; good@example.org
Action: delivered
Status: 2.0.0
Last-Attempt-Date: Mon, 02 Nov 2026 09:05:00 +0000

Final-Recipient: rfc822; gone@example.net
Action: failed
Status: 5.1.1
Diagnostic-Code: smtp;550 5.1.1 User unknown
Last-Attempt-Date: Mon, 02 Nov 2026 09:06:00 +0000

Original-Recipient: rfc822;later@example.org
Final-Recipient: rfc822;later@example.org
Action: delayed
Status: 4.4.1
Diagnostic-Code: smtp;421 Try again later

--BOUND
Content-Type: text/rfc822-headers

Received: from sender.example.com by mx.example.com
Message-ID: <original@example.com>

--BOUND--
`, "\n", "\r\n"))

	report, ok, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport() error = %v", err)
	}
	if !ok {
		t.Fatal("ParseReport() ok = false, want delivery report")
	}
	if report.EnvelopeID != "gABCDEF0123456789abcdef0123456789" {
		t.Fatalf("EnvelopeID = %q", report.EnvelopeID)
	}
	if report.MessageID != "<dsn-1@mx.example.com>" {
		t.Fatalf("MessageID = %q", report.MessageID)
	}
	if report.Arrival.IsZero() {
		t.Fatal("Arrival not parsed")
	}
	if len(report.Recipients) != 3 {
		t.Fatalf("Recipients = %#v", report.Recipients)
	}
	want := []struct {
		address string
		action  string
		status  string
	}{
		{"good@example.org", "delivered", "2.0.0"},
		{"gone@example.net", "failed", "5.1.1"},
		{"later@example.org", "delayed", "4.4.1"},
	}
	for i, w := range want {
		got := report.Recipients[i]
		if got.FinalRecipient != w.address || got.Action != w.action || got.StatusCode != w.status {
			t.Fatalf("recipient %d = %#v, want %v %v %v", i, got, w.address, w.action, w.status)
		}
	}
	if !strings.Contains(report.Recipients[1].Diagnostic, "User unknown") {
		t.Fatalf("Diagnostic = %q", report.Recipients[1].Diagnostic)
	}
	if key := ReportKey(report); key != "mid:dsn-1@mx.example.com" {
		t.Fatalf("ReportKey = %q", key)
	}
}

func TestParseReportXtextEnvelopeID(t *testing.T) {
	raw := []byte("Content-Type: multipart/report; report-type=delivery-status; boundary=B\r\n\r\n--B\r\nContent-Type: message/delivery-status\r\n\r\nOriginal-Envelope-Id: g+2Babc\r\n\r\nFinal-Recipient: rfc822;user+40example.org\r\nAction: delayed\r\nStatus: 4.0.0\r\n\r\n--B--\r\n")
	report, ok, err := ParseReport(raw)
	if err != nil || !ok {
		t.Fatalf("ParseReport() = %v, %v, %v", report, ok, err)
	}
	if report.EnvelopeID != "g+abc" {
		t.Fatalf("EnvelopeID = %q, want xtext decoded", report.EnvelopeID)
	}
	if got := report.Recipients[0].FinalRecipient; got != "user@example.org" {
		t.Fatalf("FinalRecipient = %q", got)
	}
}

func TestParseReportRejectsOtherMessages(t *testing.T) {
	cases := map[string][]byte{
		"ordinary mail":  []byte("Subject: Hello\r\n\r\nBody"),
		"MDN report":     []byte("Content-Type: multipart/report; report-type=disposition-notification; boundary=B\r\n\r\n--B--\r\n"),
		"missing status": []byte("Content-Type: multipart/report; report-type=delivery-status; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nnothing\r\n--B--\r\n"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			report, ok, err := ParseReport(raw)
			if err != nil || ok || report != nil {
				t.Fatalf("ParseReport() = %#v, %v, %v, want not a DSN", report, ok, err)
			}
		})
	}
}

func TestParseReportOneBadBlockKeepsOthers(t *testing.T) {
	raw := []byte("Content-Type: multipart/report; report-type=delivery-status; boundary=B\r\n\r\n--B\r\nContent-Type: message/delivery-status\r\n\r\nOriginal-Envelope-Id: env-1\r\n\r\nAction: bogus\r\nFinal-Recipient: rfc822;bad@example.org\r\n\r\nFinal-Recipient: rfc822;good@example.org\r\nAction: delivered\r\nStatus: 2.0.0\r\n\r\n--B--\r\n")
	report, ok, err := ParseReport(raw)
	if err != nil || !ok {
		t.Fatalf("ParseReport() = %v, %v, %v", report, ok, err)
	}
	if report.EnvelopeID != "env-1" {
		t.Fatalf("EnvelopeID = %q", report.EnvelopeID)
	}
	if len(report.Recipients) != 2 {
		t.Fatalf("Recipients = %#v", report.Recipients)
	}
	if report.Recipients[0].FinalRecipient != "bad@example.org" || report.Recipients[0].Action != "bogus" {
		t.Fatalf("first record = %#v", report.Recipients[0])
	}
}

func TestReportKeyFallbackStable(t *testing.T) {
	r1 := &Report{EnvelopeID: "env-1"}
	r2 := &Report{EnvelopeID: "env-1"}
	if ReportKey(r1) == "" || ReportKey(r1) != ReportKey(r2) || ReportKey(r1) == ReportKey(&Report{EnvelopeID: "other"}) {
		t.Fatal("fallback report key must be stable and envelope-specific")
	}
	if ReportKey(nil) != "" {
		t.Fatal("nil key")
	}
}

func TestNewEnvelopeIDIsStableShape(t *testing.T) {
	id := NewEnvelopeID()
	if len(id) != 33 || id[0] != 'g' {
		t.Fatalf("EnvelopeID = %q, want g + 32 hex", id)
	}
	for i := 1; i < len(id); i++ {
		if !isHex(id[i]) {
			t.Fatalf("EnvelopeID %q contains non-hex", id)
		}
	}
	if id == NewEnvelopeID() {
		t.Fatal("EnvelopeIDs must differ")
	}
}
