package storage

import (
	"context"
	"testing"
)

func dsnTestSend(t *testing.T, db *DB, id string, requestDSN bool, recipients ...string) (OutgoingSend, string) {
	t.Helper()
	ctx := context.Background()
	input := QueueOutgoingSendInput{
		ID: id, AccountID: "acc", DraftID: "<draft@example.com>", Transport: OutgoingTransportSMTP,
		EnvelopeFrom: "user@example.com", EnvelopeRecipients: recipients,
		MIMEData:    []byte("Message-ID: <stable@example.com>\r\n\r\nbody"),
		MessageJSON: []byte(`{"message_id":"<stable@example.com>"}`),
		RequestDSN:  requestDSN,
	}
	if requestDSN {
		input.DSNEnvelopeID = "genvid-0001"
	}
	send, err := db.QueueOutgoingSend(ctx, input)
	if err != nil {
		t.Fatalf("QueueOutgoingSend() error = %v", err)
	}
	return send, input.DSNEnvelopeID
}

func dsnReportMessage(t *testing.T, db *DB, localID int64, internetID string) {
	t.Helper()
	if _, err := db.Write().Exec(`INSERT INTO messages (id, account_id, internet_message_id, date_received)
		VALUES (?, 'acc', ?, CURRENT_TIMESTAMP)`, localID, internetID); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func dsnReportInput(messageID, envID string, records ...DSNReportRecipientInput) DSNReportInput {
	return DSNReportInput{
		ReportKey:  "mid:" + messageID,
		MessageID:  messageID,
		EnvelopeID: envID,
		Recipients: records,
	}
}

func dsnRecipient(address, action, status string) DSNReportRecipientInput {
	return DSNReportRecipientInput{Address: address, Action: action, StatusCode: status, Diagnostic: "smtp;" + status + " detail", Valid: true}
}

func TestDSNAcceptanceSnapshotsEnvelopeIncludingBcc(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "to@example.org", "bcc@example.net")

	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateRequested, send.EnvelopeRecipients); err != nil {
		t.Fatalf("RecordDSNAcceptance() error = %v", err)
	}
	got, err := db.GetOutgoingSend(ctx, send.ID)
	if err != nil || !got.DSNRequested || got.DSNState != DSNStateRequested || got.DSNEnvelopeID != "genvid-0001" {
		t.Fatalf("send = %#v, want requested DSN state", got)
	}
	recipients, err := db.DSNRecipientsForSend(ctx, send.ID)
	if err != nil {
		t.Fatalf("DSNRecipientsForSend() error = %v", err)
	}
	if len(recipients) != 2 {
		t.Fatalf("recipients = %#v, want envelope including bcc", recipients)
	}
	for _, r := range recipients {
		if r.State != DSNRecipientWaiting {
			t.Fatalf("state = %q, want waiting (not delivered, not failed)", r.State)
		}
	}

	// After the finished send releases its MIME and envelope snapshot, the DSN
	// table still correlates reports.
	if _, err := db.Write().Exec(`UPDATE outgoing_sends SET envelope_recipients='[]', mime_data=NULL, message_json='' WHERE id=?`, send.ID); err != nil {
		t.Fatal(err)
	}
	var envID string
	if err := db.Read().QueryRow(`SELECT envid FROM outgoing_dsn_envelopes WHERE send_id=?`, send.ID).Scan(&envID); err != nil || envID != "genvid-0001" {
		t.Fatalf("envelope not retained after snapshot release: %q %v", envID, err)
	}
}

func TestDSNUnsupportedServerSendsPlainState(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "to@example.org")
	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateUnsupported, send.EnvelopeRecipients); err != nil {
		t.Fatalf("RecordDSNAcceptance() error = %v", err)
	}
	got, _ := db.GetOutgoingSend(ctx, send.ID)
	if got.DSNState != DSNStateUnsupported {
		t.Fatalf("state = %q", got.DSNState)
	}
	var count int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM outgoing_dsn_recipients WHERE send_id=?`, send.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("recipients count = %d, %v, want no private snapshot when unsupported", count, err)
	}
}

func TestDSNReportCorrelationIsIdempotentAndMonotonic(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "a@example.org", "b@example.org", "c@example.org")
	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateRequested, send.EnvelopeRecipients); err != nil {
		t.Fatalf("RecordDSNAcceptance() error = %v", err)
	}
	dsnReportMessage(t, db, 101, "<r1@mx.example.com>")

	outcome, err := db.ProcessIncomingDeliveryReport(ctx, "acc", 101, dsnReportInput("<r1@mx.example.com>", "genvid-0001",
		dsnRecipient("a@example.org", "delayed", "4.4.1"),
		dsnRecipient("b@example.org", "delivered", "2.0.0"),
		dsnRecipient("stranger@example.net", "failed", "5.1.1"),
		DSNReportRecipientInput{Address: "garbage", Action: "failed", Valid: false},
	))
	if err != nil {
		t.Fatalf("ProcessIncomingDeliveryReport() error = %v", err)
	}
	if outcome.Kind != dsnMarkerCorrelated || outcome.Applied != 2 {
		t.Fatalf("outcome = %#v, want 2 applied, stranger and invalid ignored", outcome)
	}
	states := dsnStateMap(t, db, send.ID)
	if states["a@example.org"] != DSNRecipientDelayed || states["b@example.org"] != DSNRecipientDelivered || states["c@example.org"] != DSNRecipientWaiting {
		t.Fatalf("states = %#v", states)
	}

	// A late delayed notice cannot move a confirmed delivery back.
	dsnReportMessage(t, db, 102, "<r2@mx.example.com>")
	outcome, err = db.ProcessIncomingDeliveryReport(ctx, "acc", 102, dsnReportInput("<r2@mx.example.com>", "genvid-0001",
		dsnRecipient("b@example.org", "delayed", "4.4.1"),
		dsnRecipient("a@example.org", "delivered", "2.0.0"),
		dsnRecipient("c@example.org", "failed", "5.0.0"),
	))
	if err != nil || outcome.Applied != 2 {
		t.Fatalf("second report outcome = %#v, %v", outcome, err)
	}
	states = dsnStateMap(t, db, send.ID)
	if states["a@example.org"] != DSNRecipientDelivered || states["b@example.org"] != DSNRecipientDelivered || states["c@example.org"] != DSNRecipientFailed {
		t.Fatalf("states after second report = %#v", states)
	}

	// Reopening the first message neither reapplies nor duplicates anything.
	outcome, err = db.ProcessIncomingDeliveryReport(ctx, "acc", 101, dsnReportInput("<r1@mx.example.com>", "genvid-0001",
		dsnRecipient("a@example.org", "failed", "5.0.0"),
	))
	if err != nil || outcome.Applied != 0 || outcome.Kind != dsnMarkerCorrelated {
		t.Fatalf("reopen outcome = %#v, %v", outcome, err)
	}
	if got := dsnStateMap(t, db, send.ID)["a@example.org"]; got != DSNRecipientDelivered {
		t.Fatalf("reopen changed delivered state to %q", got)
	}

	// A duplicate delivery copy of the first report associates, never reapplies.
	dsnReportMessage(t, db, 103, "<r1-copy@mx.example.com>")
	outcome, err = db.ProcessIncomingDeliveryReport(ctx, "acc", 103, dsnReportInput("<r1@mx.example.com>", "genvid-0001",
		dsnRecipient("c@example.org", "delivered", "2.0.0"),
	))
	if err != nil || outcome.Applied != 0 {
		t.Fatalf("duplicate copy outcome = %#v, %v", outcome, err)
	}
}

func TestDSNUnknownEnvelopeStaysOrdinaryMessage(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "a@example.org")
	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateRequested, send.EnvelopeRecipients); err != nil {
		t.Fatal(err)
	}
	dsnReportMessage(t, db, 201, "<foreign@mx.example.com>")
	outcome, err := db.ProcessIncomingDeliveryReport(ctx, "acc", 201, dsnReportInput("<foreign@mx.example.com>", "other-env",
		dsnRecipient("a@example.org", "failed", "5.0.0"),
	))
	if err != nil || outcome.Kind != dsnMarkerUnrecognized {
		t.Fatalf("outcome = %#v, %v, want unrecognized ordinary mail", outcome, err)
	}
	if got := dsnStateMap(t, db, send.ID)["a@example.org"]; got != DSNRecipientWaiting {
		t.Fatalf("unrelated report rewrote send state to %q", got)
	}
}

func TestDSNEnvelopeIDStableAcrossRequeue(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Write().Exec(`INSERT INTO messages (id, account_id, date_received) VALUES (501, 'acc', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	input := QueueOutgoingSendInput{
		ID: "send-1", AccountID: "acc", MessageID: 501, DraftID: "<draft@example.com>", Transport: OutgoingTransportSMTP,
		EnvelopeFrom: "user@example.com", EnvelopeRecipients: []string{"a@example.org"},
		MIMEData:    []byte("Message-ID: <stable@example.com>\r\n\r\nbody"),
		MessageJSON: []byte(`{"message_id":"<stable@example.com>"}`),
		RequestDSN:  true, DSNEnvelopeID: "genvid-0001",
	}
	send, err := db.QueueOutgoingSend(ctx, input)
	if err != nil {
		t.Fatalf("QueueOutgoingSend() error = %v", err)
	}
	first := send.DSNEnvelopeID
	if first == "" {
		t.Fatal("expected envelope id at queue time")
	}
	// Pending-draft edit requeues the same message id with a freshly generated
	// id from the caller; the stored id must remain the original one.
	if _, err := db.QueueOutgoingSend(ctx, QueueOutgoingSendInput{
		ID: send.ID, AccountID: "acc", MessageID: send.MessageID, DraftID: send.DraftID,
		Transport: send.Transport, EnvelopeFrom: send.EnvelopeFrom, EnvelopeRecipients: send.EnvelopeRecipients,
		MIMEData: send.MIMEData, MessageJSON: send.MessageJSON,
		RequestDSN: true, DSNEnvelopeID: "genvid-NEW",
	}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	got, _ := db.GetOutgoingSend(ctx, send.ID)
	if got.DSNEnvelopeID != first {
		t.Fatalf("envid changed on pending requeue: %q vs %q", got.DSNEnvelopeID, first)
	}
}

func TestDSNCascadesWithSendRetention(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "a@example.org")
	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateRequested, send.EnvelopeRecipients); err != nil {
		t.Fatal(err)
	}
	dsnReportMessage(t, db, 301, "<r@mx.example.com>")
	if _, err := db.ProcessIncomingDeliveryReport(ctx, "acc", 301, dsnReportInput("<r@mx.example.com>", "genvid-0001",
		dsnRecipient("a@example.org", "delivered", "2.0.0"))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`DELETE FROM outgoing_sends WHERE id=?`, send.ID); err != nil {
		t.Fatal(err)
	}
	for table, query := range map[string]string{
		"envelopes":  `SELECT COUNT(*) FROM outgoing_dsn_envelopes WHERE send_id=?`,
		"recipients": `SELECT COUNT(*) FROM outgoing_dsn_recipients WHERE send_id=?`,
	} {
		var count int
		if err := db.Read().QueryRow(query, send.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s remain after retention delete: %d, %v", table, count, err)
		}
	}
	var reportCount int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM outgoing_dsn_reports WHERE send_id=?`, send.ID).Scan(&reportCount); err != nil || reportCount != 0 {
		t.Fatalf("report association remains: %d, %v", reportCount, err)
	}
}

func TestDeliveryStatusForMessageAnchorsSentAndReport(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	send, _ := dsnTestSend(t, db, "send-1", true, "a@example.org")
	if err := db.RecordDSNAcceptance(ctx, send.ID, DSNStateRequested, send.EnvelopeRecipients); err != nil {
		t.Fatal(err)
	}
	// Finished send: MIME cleared and the Sent message is a new row keyed by
	// the Internet Message-ID.
	if _, err := db.Write().Exec(`UPDATE outgoing_sends SET status=?, sent_message_id='<stable@example.com>',
		envelope_recipients='[]', mime_data=NULL, message_json='' WHERE id=?`, OutgoingSendSent, send.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`INSERT INTO messages (id, account_id, internet_message_id, date_received)
		VALUES (401, 'acc', '<stable@example.com>', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	view, err := db.DeliveryStatusForMessage(ctx, "default", 401)
	if err != nil || view.Kind != "sent" || view.DSNState != DSNStateRequested || len(view.Recipients) != 1 {
		t.Fatalf("sent view = %#v, %v", view, err)
	}

	dsnReportMessage(t, db, 402, "<r@mx.example.com>")
	if _, err := db.ProcessIncomingDeliveryReport(ctx, "acc", 402, dsnReportInput("<r@mx.example.com>", "genvid-0001",
		dsnRecipient("a@example.org", "failed", "5.1.1"))); err != nil {
		t.Fatal(err)
	}
	reportView, err := db.DeliveryStatusForMessage(ctx, "default", 402)
	if err != nil || reportView.Kind != "report" || len(reportView.Recipients) != 1 || reportView.Recipients[0].State != DSNRecipientFailed {
		t.Fatalf("report view = %#v, %v", reportView, err)
	}

	empty, err := db.DeliveryStatusForMessage(ctx, "default", 999)
	if err != nil || empty.Kind != "" {
		t.Fatalf("empty view = %#v, %v", empty, err)
	}
}

func TestDraftDSNOptionRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Write().Exec(`INSERT INTO messages (id, account_id, date_received) VALUES (601, 'acc', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if db.GetDraftDSNOption(ctx, 601) {
		t.Fatal("default draft option must be off")
	}
	if err := db.SetDraftDSNOption(ctx, 601, true); err != nil {
		t.Fatalf("SetDraftDSNOption() error = %v", err)
	}
	if !db.GetDraftDSNOption(ctx, 601) {
		t.Fatal("draft option did not persist")
	}
	if err := db.SetDraftDSNOption(ctx, 601, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if db.GetDraftDSNOption(ctx, 601) {
		t.Fatal("draft option did not turn off")
	}
}

func dsnStateMap(t *testing.T, db *DB, sendID string) map[string]string {
	t.Helper()
	recipients, err := db.DSNRecipientsForSend(context.Background(), sendID)
	if err != nil {
		t.Fatalf("DSNRecipientsForSend() error = %v", err)
	}
	states := make(map[string]string, len(recipients))
	for _, r := range recipients {
		states[r.Address] = r.State
	}
	return states
}
