package handler

import (
	"context"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/config"
	mailmessage "github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	"github.com/cristianadrielbraun/gofer/internal/store"
)

func newDSNProcessorHandler(t *testing.T) (*Handler, *storage.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{{ID: "acc_inbox", AccountID: "acc", Name: "Inbox", Role: "inbox", Selectable: true}}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	h := &Handler{db: db, blobStore: store.NewBlobStore(filepath.Join(t.TempDir(), "blobs"))}
	return h, db
}

func insertIncomingMessage(t *testing.T, db *storage.DB, id int64, internetID string) {
	t.Helper()
	if _, err := db.Write().Exec(`INSERT INTO messages (id, account_id, internet_message_id, subject, from_email, date_received)
		VALUES (?, 'acc', ?, 'Delivery report', 'mailer-daemon@mx.example.com', CURRENT_TIMESTAMP)`,
		id, internetID); err != nil {
		t.Fatalf("insert report message: %v", err)
	}
}

func dsnReportMIME(messageID, envID string, recipients ...string) []byte {
	var b strings.Builder
	b.WriteString("From: Mail Delivery System <mailer-daemon@mx.example.com>\r\n")
	b.WriteString("To: <user@example.com>\r\n")
	b.WriteString("Subject: Delivery Status Notification\r\n")
	b.WriteString("Message-ID: <" + messageID + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=B\r\n\r\n")
	b.WriteString("--B\r\nContent-Type: text/plain\r\n\r\nReport\r\n")
	b.WriteString("--B\r\nContent-Type: message/delivery-status\r\n\r\n")
	b.WriteString("Reporting-MTA: dns; mx.example.com\r\nOriginal-Envelope-Id: " + envID + "\r\n\r\n")
	for _, line := range recipients {
		b.WriteString(line + "\r\n\r\n")
	}
	b.WriteString("--B--\r\n")
	return []byte(b.String())
}

func TestProcessOpenedDeliveryReportEndToEnd(t *testing.T) {
	ctx := context.Background()
	h, db := newDSNProcessorHandler(t)

	queued, err := db.QueueOutgoingSend(ctx, storage.QueueOutgoingSendInput{
		ID: "send-1", AccountID: "acc", DraftID: "<draft@example.com>", Transport: storage.OutgoingTransportSMTP,
		EnvelopeFrom:       "user@example.com",
		EnvelopeRecipients: []string{"good@example.org", "secret-bcc@example.net"},
		MIMEData:           []byte("Message-ID: <sent@example.com>\r\n\r\nbody"),
		MessageJSON:        []byte(`{"message_id":"<sent@example.com>"}`),
		RequestDSN:         true,
		DSNEnvelopeID:      "genvid-e2e",
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := db.RecordDSNAcceptance(ctx, queued.ID, storage.DSNStateRequested, queued.EnvelopeRecipients); err != nil {
		t.Fatalf("acceptance: %v", err)
	}

	insertIncomingMessage(t, db, 701, "<r701@mx.example.com>")
	raw := dsnReportMIME("r701@mx.example.com", "genvid-e2e",
		"Final-Recipient: rfc822; good@example.org\r\nAction: delivered\r\nStatus: 2.0.0",
		"Final-Recipient: rfc822; secret-bcc@example.net\r\nAction: delayed\r\nStatus: 4.4.1\r\nDiagnostic-Code: smtp;421 try later",
	)
	rawPath, err := h.blobStore.StoreRaw(ctx, "acc", 701, raw)
	if err != nil {
		t.Fatalf("StoreRaw() error = %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `UPDATE messages SET raw_path=? WHERE id=701`, rawPath); err != nil {
		t.Fatal(err)
	}

	h.processOpenedDeliveryReport(ctx, db, "default", 701, "acc", rawPath)

	recipients, err := db.DSNRecipientsForSend(ctx, queued.ID)
	if err != nil {
		t.Fatalf("recipients: %v", err)
	}
	states := map[string]string{}
	for _, r := range recipients {
		states[r.Address] = r.State
	}
	if states["good@example.org"] != storage.DSNRecipientDelivered {
		t.Fatalf("good state = %q, want delivered", states["good@example.org"])
	}
	if states["secret-bcc@example.net"] != storage.DSNRecipientDelayed {
		t.Fatalf("bcc state = %q, want delayed", states["secret-bcc@example.net"])
	}

	// Reopening neither reprocesses nor changes anything.
	h.processOpenedDeliveryReport(ctx, db, "default", 701, "acc", rawPath)
	recipients, _ = db.DSNRecipientsForSend(ctx, queued.ID)
	for _, r := range recipients {
		if r.Address == "good@example.org" && r.State != storage.DSNRecipientDelivered {
			t.Fatalf("reopen changed state to %q", r.State)
		}
	}

	view, err := db.DeliveryStatusForMessage(ctx, "default", 701)
	if err != nil || view.Kind != "report" || len(view.Recipients) != 2 {
		t.Fatalf("report view = %#v, %v", view, err)
	}

	// A late failure cannot move the confirmed delivery back.
	insertIncomingMessage(t, db, 702, "<r702@mx.example.com>")
	raw2 := dsnReportMIME("r702@mx.example.com", "genvid-e2e",
		"Final-Recipient: rfc822; good@example.org\r\nAction: failed\r\nStatus: 5.0.0",
	)
	rawPath2, _ := h.blobStore.StoreRaw(ctx, "acc", 702, raw2)
	_, _ = db.Write().ExecContext(ctx, `UPDATE messages SET raw_path=? WHERE id=702`, rawPath2)
	h.processOpenedDeliveryReport(ctx, db, "default", 702, "acc", rawPath2)
	recipients, _ = db.DSNRecipientsForSend(ctx, queued.ID)
	for _, r := range recipients {
		if r.Address == "good@example.org" && r.State != storage.DSNRecipientDelivered {
			t.Fatalf("late failure moved delivered state to %q", r.State)
		}
	}
}

func TestProcessOpenedDeliveryReportUnknownEnvelopeStaysOrdinary(t *testing.T) {
	ctx := context.Background()
	h, db := newDSNProcessorHandler(t)

	queued, err := db.QueueOutgoingSend(ctx, storage.QueueOutgoingSendInput{
		ID: "send-1", AccountID: "acc", DraftID: "<draft@example.com>", Transport: storage.OutgoingTransportSMTP,
		EnvelopeFrom:       "user@example.com",
		EnvelopeRecipients: []string{"good@example.org"},
		MIMEData:           []byte("Message-ID: <sent@example.com>\r\n\r\nbody"),
		MessageJSON:        []byte(`{"message_id":"<sent@example.com>"}`),
		RequestDSN:         true,
		DSNEnvelopeID:      "genvid-known",
	})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := db.RecordDSNAcceptance(ctx, queued.ID, storage.DSNStateRequested, queued.EnvelopeRecipients); err != nil {
		t.Fatalf("acceptance: %v", err)
	}

	insertIncomingMessage(t, db, 801, "<r801@mx.example.com>")
	raw := dsnReportMIME("r801@mx.example.com", "genvid-unknown",
		"Final-Recipient: rfc822; good@example.org\r\nAction: failed\r\nStatus: 5.0.0",
	)
	rawPath, _ := h.blobStore.StoreRaw(ctx, "acc", 801, raw)
	_, _ = db.Write().ExecContext(ctx, `UPDATE messages SET raw_path=? WHERE id=801`, rawPath)
	h.processOpenedDeliveryReport(ctx, db, "default", 801, "acc", rawPath)

	view, err := db.DeliveryStatusForMessage(ctx, "default", 801)
	if err != nil || view.Kind != "" {
		t.Fatalf("unknown report view = %#v, %v, want ordinary message", view, err)
	}
	states := map[string]string{}
	recipients, _ := db.DSNRecipientsForSend(ctx, queued.ID)
	for _, r := range recipients {
		states[r.Address] = r.State
	}
	if states["good@example.org"] != storage.DSNRecipientWaiting {
		t.Fatalf("envelope state changed to %q", states["good@example.org"])
	}
}

func TestFormRequestsDSN(t *testing.T) {
	if formRequestsDSN(blankForm{}) {
		t.Fatal("default must be off")
	}
	for _, value := range []string{"1", "true", "on", "yes", "TRUE"} {
		if !formRequestsDSN(simpleForm{value: value}) {
			t.Fatalf("value %q not recognized as requested", value)
		}
	}
	if formRequestsDSN(simpleForm{value: "0"}) {
		t.Fatal("0 must mean off")
	}
}

type blankForm struct{}

func (blankForm) FormValue(string) string { return "" }

type simpleForm struct{ value string }

func (f simpleForm) FormValue(string) string { return f.value }

func TestQueueOutgoingMessageBindsDSNOnlyForSMTP(t *testing.T) {
	ctx := context.Background()
	_, db := newDSNProcessorHandler(t)
	accountStore, err := config.NewAccountStore(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewAccountStore() error = %v", err)
	}
	h := &Handler{db: db, accountStore: accountStore}

	requested := &mailmessage.OutgoingMessage{
		FromEmail:  "user@example.com",
		To:         []*mail.Address{{Address: "friend@example.org"}},
		Subject:    "Scheduled",
		MessageID:  "<scheduled@example.com>",
		TextBody:   "hello",
		RequestDSN: true,
	}
	sendAfter := time.Now().Add(time.Hour)
	queued, err := h.queueOutgoingMessage(ctx, "acc", 0, "<scheduled@example.com>", requested, sendAfter, true)
	if err != nil {
		t.Fatalf("queueOutgoingMessage() error = %v", err)
	}
	if !queued.DSNRequested || queued.DSNEnvelopeID == "" || queued.Transport != storage.OutgoingTransportSMTP {
		t.Fatalf("queued = %#v, want SMTP DSN request with envelope id", queued)
	}

	// ENVID stability across pending-draft edits is covered at the storage
	// layer; opt-out and non-SMTP transports never carry a request.
	if ok, id := resolveDSNEnqueue(requested, storage.OutgoingTransportGmail); ok || id != "" {
		t.Fatalf("gmail DSN = %v, %q, want no request", ok, id)
	}
	off := *requested
	off.RequestDSN = false
	if ok, id := resolveDSNEnqueue(&off, storage.OutgoingTransportSMTP); ok || id != "" {
		t.Fatalf("opt-out DSN = %v, %q, want no request", ok, id)
	}
}
