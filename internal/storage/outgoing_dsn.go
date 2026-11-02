package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Delivery Status Notification (DSN, RFC 3461/3464) support.
//
// A user opt-in send can ask an SMTP server to bind an envelope identifier
// (ENVID) to the transaction and request per-recipient reports (NOTIFY). The
// server accepting the message is not delivery, so the acceptance result and
// later reports are stored separately. These private tables keep the envelope
// - including Bcc recipients, which never appear in the sent message - for as
// long as the outgoing_sends row is retained, and disappear with it.

const (
	// DSNStateRequested means the SMTP server advertised DSN and the ENVID and
	// per-recipient NOTIFY parameters were sent with the accepted transaction.
	DSNStateRequested = "requested"
	// DSNStateUnsupported means the user asked for reports but the SMTP server
	// did not advertise DSN; the message went out as an ordinary message.
	DSNStateUnsupported = "unsupported"
)

const (
	DSNRecipientWaiting   = "waiting"
	DSNRecipientDelayed   = "delayed"
	DSNRecipientDelivered = "delivered"
	DSNRecipientFailed    = "failed"
)

const (
	dsnMarkerCorrelated   = "correlated"
	dsnMarkerUnrecognized = "unrecognized"
	dsnMarkerNotReport    = "not_report"
)

// DSNRecipient is one envelope recipient's latest reported delivery state.
type DSNRecipient struct {
	Address         string
	State           string
	StatusCode      string
	Diagnostic      string
	ReportMessageID string
	ReportedAt      time.Time
	UpdatedAt       time.Time
}

// DSNReportRecipientInput is one parsed per-recipient record of a delivery
// status report. Valid reports may still contain unparseable or irrelevant
// records; Valid lets the processor skip a record without discarding the rest.
type DSNReportRecipientInput struct {
	Address    string
	Action     string
	StatusCode string
	Diagnostic string
	ReportedAt time.Time
	Valid      bool
}

// DSNReportInput is the provider-neutral content of a multipart/report DSN.
type DSNReportInput struct {
	// ReportKey identifies one logical report, so a redelivered copy or a
	// second open can never be counted twice.
	ReportKey string
	// MessageID is the inbound report message's own Message-ID header.
	MessageID string
	// EnvelopeID is the Original-Envelope-Id the report names.
	EnvelopeID string
	Recipients []DSNReportRecipientInput
}

// DSNReportOutcome reports what processing an opened message produced.
type DSNReportOutcome struct {
	// Kind is "", dsnMarkerCorrelated or dsnMarkerUnrecognized/not_report.
	Kind   string
	SendID string
	// Applied counts recipient records that changed or confirmed stored state.
	Applied int
}

// DeliveryStatusView is the owner-scoped delivery detail shown for an opened
// message: the Sent message of a DSN send, or an inbound correlated report.
type DeliveryStatusView struct {
	Kind       string // "sent" or "report"
	SendID     string
	DSNState   string
	Subject    string
	Recipients []DSNRecipient
}

func migrateV133ToV134(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 134 {
		return nil
	}
	// Stripped migration fixtures may contain neither table; DDL with
	// foreign keys is then skipped without affecting the schemas under test.
	hasOutgoing, err := tableExistsTx(tx, "outgoing_sends")
	if err != nil {
		return err
	}
	if hasOutgoing {
		columns := []struct {
			name string
			sql  string
		}{
			{"dsn_requested", `ALTER TABLE outgoing_sends ADD COLUMN dsn_requested INTEGER NOT NULL DEFAULT 0 CHECK (dsn_requested IN (0, 1))`},
			{"dsn_envid", `ALTER TABLE outgoing_sends ADD COLUMN dsn_envid TEXT NOT NULL DEFAULT ''`},
			{"dsn_state", `ALTER TABLE outgoing_sends ADD COLUMN dsn_state TEXT NOT NULL DEFAULT '' CHECK (dsn_state IN ('', 'requested', 'unsupported'))`},
		}
		for _, column := range columns {
			exists, err := columnExistsTx(tx, "outgoing_sends", column.name)
			if err != nil {
				return err
			}
			if !exists {
				if _, err := tx.Exec(column.sql); err != nil {
					return err
				}
			}
		}
	}
	statements := []string{}
	if hasMessages, err := tableExistsTx(tx, "messages"); err != nil {
		return err
	} else if hasMessages {
		statements = append(statements,
			// Compose-level request that travels with the draft. The sent
			// message never carries it; only the queued send does.
			`CREATE TABLE IF NOT EXISTS draft_send_options (
				message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
				dsn_requested INTEGER NOT NULL DEFAULT 0 CHECK (dsn_requested IN (0, 1)),
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`)
	}
	if hasOutgoing {
		statements = append(statements,
			// One envelope a DSN send asked about. Survives the MIME/snapshot
			// cleanup so reports can still be correlated during send retention.
			`CREATE TABLE IF NOT EXISTS outgoing_dsn_envelopes (
				send_id TEXT PRIMARY KEY REFERENCES outgoing_sends(id) ON DELETE CASCADE,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				envid TEXT NOT NULL CHECK (envid <> ''),
				envelope_from TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_outgoing_dsn_envelopes_lookup
				ON outgoing_dsn_envelopes(account_id, envid)`,
			// The private envelope recipient list, including Bcc addresses. It
			// is never written to message headers or bodies.
			`CREATE TABLE IF NOT EXISTS outgoing_dsn_recipients (
				send_id TEXT NOT NULL REFERENCES outgoing_sends(id) ON DELETE CASCADE,
				address TEXT NOT NULL,
				state TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'delayed', 'delivered', 'failed')),
				status_code TEXT NOT NULL DEFAULT '',
				diagnostic TEXT NOT NULL DEFAULT '',
				report_message_id TEXT NOT NULL DEFAULT '',
				reported_at DATETIME,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (send_id, address)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_outgoing_dsn_recipients_send
				ON outgoing_dsn_recipients(send_id, state)`,
			// Exactly one processing result per logical report and per opened
			// message, so reopening, duplicate delivery, or a restart cannot
			// accumulate a report twice.
			`CREATE TABLE IF NOT EXISTS outgoing_dsn_reports (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				message_local_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
				report_key TEXT NOT NULL CHECK (report_key <> ''),
				send_id TEXT REFERENCES outgoing_sends(id) ON DELETE CASCADE,
				envid TEXT NOT NULL DEFAULT '',
				correlation TEXT NOT NULL CHECK (correlation IN ('correlated', 'unrecognized', 'not_report')),
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_outgoing_dsn_reports_key
				ON outgoing_dsn_reports(account_id, report_key)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_outgoing_dsn_reports_message
				ON outgoing_dsn_reports(message_local_id) WHERE message_local_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_outgoing_dsn_reports_send
				ON outgoing_dsn_reports(send_id) WHERE send_id IS NOT NULL`,
		)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if err := markSchemaVersion(tx, 134); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDraftDSNOption persists the compose request with a draft. It is local
// metadata only and never becomes part of the draft's MIME.
func (db *DB) SetDraftDSNOption(ctx context.Context, messageID int64, requested bool) error {
	if messageID <= 0 {
		return fmt.Errorf("draft id is required")
	}
	value := 0
	if requested {
		value = 1
	}
	_, err := db.Write().ExecContext(ctx, `
		INSERT INTO draft_send_options (message_id, dsn_requested, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(message_id) DO UPDATE SET dsn_requested = excluded.dsn_requested, updated_at = CURRENT_TIMESTAMP`,
		messageID, value)
	return err
}

// GetDraftDSNOption reports whether a draft asks for delivery reports.
func (db *DB) GetDraftDSNOption(ctx context.Context, messageID int64) bool {
	if messageID <= 0 {
		return false
	}
	var requested int
	if err := db.Read().QueryRowContext(ctx, `SELECT dsn_requested FROM draft_send_options WHERE message_id = ?`, messageID).Scan(&requested); err != nil {
		return false
	}
	return requested != 0
}

// RecordDSNAcceptance records what the accepting SMTP server actually granted
// for a DSN request. It is called once the server's acceptance result is
// known, including an ambiguous DATA response (reports may still arrive).
//
// When the server supports DSN it snapshots the envelope - recipients and
// all - so later reports correlate after MIME and snapshot cleanup. Retries
// reuse the same envelope id and never downgrade a state already reported.
func (db *DB) RecordDSNAcceptance(ctx context.Context, sendID string, state string, recipients []string) error {
	if sendID == "" {
		return fmt.Errorf("send id is required")
	}
	if state != DSNStateRequested && state != DSNStateUnsupported {
		return fmt.Errorf("invalid DSN state %q", state)
	}
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var accountID, envID string
	if err := tx.QueryRowContext(ctx, `SELECT account_id, dsn_envid FROM outgoing_sends WHERE id = ?`, sendID).Scan(&accountID, &envID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outgoing_sends SET dsn_state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, state, sendID); err != nil {
		return err
	}
	// An earlier ambiguous attempt may already have negotiated DSN with the
	// same ENVID and earned recipient states. An unsupported answer from the
	// next server must not discard that: reports for the ENVID may still come.
	if state == DSNStateUnsupported {
		return tx.Commit()
	}
	if envID == "" {
		return fmt.Errorf("accepted DSN send %s has no envelope id", sendID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outgoing_dsn_envelopes (send_id, account_id, envid)
		VALUES (?, ?, ?)`, sendID, accountID, envID); err != nil {
		return err
	}
	for _, recipient := range recipients {
		address := normalizeDSNAddress(recipient)
		if address == "" {
			continue
		}
		// Keep states an earlier attempt already earned; the same ENVID binds
		// retries, so a late report from either attempt matches this row.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO outgoing_dsn_recipients (send_id, address, state)
			VALUES (?, ?, ?)
			ON CONFLICT(send_id, address) DO NOTHING`, sendID, address, DSNRecipientWaiting); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ProcessIncomingDeliveryReport correlates an opened multipart/report message
// to the send its Original-Envelope-Id names and applies its valid recipient
// records. Everything happens in one transaction: if the commit fails, no
// marker is kept and reopening the cached message repeats the work. The
// original message is never modified or resent.
func (db *DB) ProcessIncomingDeliveryReport(ctx context.Context, accountID string, localMessageID int64, report DSNReportInput) (DSNReportOutcome, error) {
	if accountID == "" || localMessageID <= 0 {
		return DSNReportOutcome{}, fmt.Errorf("account and message are required")
	}
	key := strings.TrimSpace(report.ReportKey)
	if key == "" {
		return DSNReportOutcome{}, fmt.Errorf("report key is required")
	}
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return DSNReportOutcome{}, err
	}
	defer tx.Rollback()

	// This exact message was opened before: its outcome stands as recorded.
	var existingKind, existingSend string
	if err := tx.QueryRowContext(ctx, `SELECT correlation, COALESCE(send_id, '') FROM outgoing_dsn_reports WHERE message_local_id = ?`, localMessageID).Scan(&existingKind, &existingSend); err == nil {
		return DSNReportOutcome{Kind: existingKind, SendID: existingSend}, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DSNReportOutcome{}, err
	}

	// A second delivery copy of an already processed report mirrors the
	// earlier outcome but never reapplies its records. Its own marker uses a
	// message-local key, so the logical-report key stays unique.
	var priorSend, priorCorrelation string
	switch err := tx.QueryRowContext(ctx, `SELECT correlation, COALESCE(send_id, '') FROM outgoing_dsn_reports WHERE account_id = ? AND report_key = ? AND correlation != ?`, accountID, key, dsnMarkerNotReport).Scan(&priorCorrelation, &priorSend); {
	case err == nil:
		associationKey := fmt.Sprintf("assoc:%d", localMessageID)
		if err := insertDSNMarker(ctx, tx, accountID, localMessageID, associationKey, priorSend, report.EnvelopeID, priorCorrelation); err != nil {
			return DSNReportOutcome{}, err
		}
		return DSNReportOutcome{Kind: priorCorrelation, SendID: priorSend}, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return DSNReportOutcome{}, err
	}

	envID := normalizeEnvelopeID(report.EnvelopeID)
	var sendID, envelopeFrom string
	switch err := tx.QueryRowContext(ctx, `SELECT send_id, envelope_from FROM outgoing_dsn_envelopes WHERE account_id = ? AND envid = ?`, accountID, envID).Scan(&sendID, &envelopeFrom); {
	case errors.Is(err, sql.ErrNoRows):
		// Unknown identifier: stay an ordinary message and touch no send.
		if err := insertDSNMarker(ctx, tx, accountID, localMessageID, key, "", report.EnvelopeID, dsnMarkerUnrecognized); err != nil {
			return DSNReportOutcome{}, err
		}
		return DSNReportOutcome{Kind: dsnMarkerUnrecognized}, tx.Commit()
	case err != nil:
		return DSNReportOutcome{}, err
	}

	known := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT address FROM outgoing_dsn_recipients WHERE send_id = ?`, sendID)
	if err != nil {
		return DSNReportOutcome{}, err
	}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			rows.Close()
			return DSNReportOutcome{}, err
		}
		known[address] = true
	}
	if err := rows.Close(); err != nil {
		return DSNReportOutcome{}, err
	}

	applied := 0
	for _, record := range report.Recipients {
		address := normalizeDSNAddress(record.Address)
		if !record.Valid || address == "" || !known[address] {
			// Malformed, uninteresting, or foreign to this envelope: leave the
			// record as readable message content, not a state change.
			continue
		}
		changed, err := applyDSNRecipientRecord(ctx, tx, sendID, address, report.MessageID, record)
		if err != nil {
			return DSNReportOutcome{}, err
		}
		if changed {
			applied++
		}
	}
	if err := insertDSNMarker(ctx, tx, accountID, localMessageID, key, sendID, report.EnvelopeID, dsnMarkerCorrelated); err != nil {
		return DSNReportOutcome{}, err
	}
	return DSNReportOutcome{Kind: dsnMarkerCorrelated, SendID: sendID, Applied: applied}, tx.Commit()
}

// MarkMessageNotDeliveryReport records that an opened message was examined and
// is not a delivery report, so reopening it need not re-read its MIME.
func (db *DB) MarkMessageNotDeliveryReport(ctx context.Context, accountID string, localMessageID int64) error {
	if accountID == "" || localMessageID <= 0 {
		return nil
	}
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM outgoing_dsn_reports WHERE message_local_id = ?`, localMessageID).Scan(&present); err != nil {
		return err
	}
	if present > 0 {
		return tx.Commit()
	}
	key := fmt.Sprintf("not-report:%d", localMessageID)
	if err := insertDSNMarker(ctx, tx, accountID, localMessageID, key, "", "", dsnMarkerNotReport); err != nil {
		return err
	}
	return tx.Commit()
}

func insertDSNMarker(ctx context.Context, tx *sql.Tx, accountID string, localMessageID int64, key, sendID, envID, correlation string) error {
	var sendArg any
	if strings.TrimSpace(sendID) != "" {
		sendArg = sendID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO outgoing_dsn_reports (account_id, message_local_id, report_key, send_id, envid, correlation)
		VALUES (?, ?, ?, ?, ?, ?)`,
		accountID, localMessageID, key, sendArg, envID, correlation)
	return err
}

// applyDSNRecipientRecord performs the monotonic per-recipient transition.
// Delivered and failed are terminal; a late delayed notice cannot move them
// back to waiting. Repeating the current state is a confirmed no-op.
func applyDSNRecipientRecord(ctx context.Context, tx *sql.Tx, sendID, address, reportMessageID string, record DSNReportRecipientInput) (bool, error) {
	state := dsnStateForAction(record.Action)
	if state == "" {
		// relayed/expanded: neither success nor failure; keep waiting.
		return false, nil
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM outgoing_dsn_recipients WHERE send_id = ? AND address = ?`, sendID, address).Scan(&current); err != nil {
		return false, err
	}
	if !dsnTransitionAllowed(current, state) {
		return false, nil
	}
	var reportedAt any
	if !record.ReportedAt.IsZero() {
		reportedAt = record.ReportedAt.UTC()
	}
	diagnostic := truncateDSNText(record.Diagnostic, 1000)
	statusCode := truncateDSNText(record.StatusCode, 32)
	messageID := truncateDSNText(strings.TrimSpace(reportMessageID), 512)
	if _, err := tx.ExecContext(ctx, `
		UPDATE outgoing_dsn_recipients
		SET state = ?, status_code = ?, diagnostic = ?, report_message_id = ?, reported_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE send_id = ? AND address = ?`,
		state, statusCode, diagnostic, messageID, reportedAt, sendID, address); err != nil {
		return false, err
	}
	return true, nil
}

func dsnStateForAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "delivered":
		return DSNRecipientDelivered
	case "failed":
		return DSNRecipientFailed
	case "delayed":
		return DSNRecipientDelayed
	default:
		// "relayed" and "expanded" carry no final delivery meaning.
		return ""
	}
}

func dsnTransitionAllowed(current, next string) bool {
	switch current {
	case DSNRecipientDelivered, DSNRecipientFailed:
		return false
	case DSNRecipientDelayed:
		return next == DSNRecipientDelivered || next == DSNRecipientFailed || next == DSNRecipientDelayed
	default: // waiting
		return next == DSNRecipientDelayed || next == DSNRecipientDelivered || next == DSNRecipientFailed
	}
}

// DSNRecipientsForSend returns the stored recipient states for one send.
func (db *DB) DSNRecipientsForSend(ctx context.Context, sendID string) ([]DSNRecipient, error) {
	return db.queryDSNRecipients(ctx, `
		SELECT address, state, status_code, diagnostic, report_message_id, reported_at, updated_at
		FROM outgoing_dsn_recipients WHERE send_id = ? ORDER BY address`, sendID)
}

// DSNRecipientsForSends batches recipient states for several sends.
func (db *DB) DSNRecipientsForSends(ctx context.Context, sendIDs []string) (map[string][]DSNRecipient, error) {
	result := make(map[string][]DSNRecipient)
	for _, sendID := range sendIDs {
		if strings.TrimSpace(sendID) == "" {
			continue
		}
		recipients, err := db.DSNRecipientsForSend(ctx, sendID)
		if err != nil {
			return nil, err
		}
		if len(recipients) > 0 {
			result[sendID] = recipients
		}
	}
	return result, nil
}

func (db *DB) queryDSNRecipients(ctx context.Context, query string, args ...any) ([]DSNRecipient, error) {
	rows, err := db.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipients []DSNRecipient
	for rows.Next() {
		var recipient DSNRecipient
		var reported sql.NullTime
		if err := rows.Scan(&recipient.Address, &recipient.State, &recipient.StatusCode, &recipient.Diagnostic, &recipient.ReportMessageID, &reported, &recipient.UpdatedAt); err != nil {
			return nil, err
		}
		if reported.Valid {
			recipient.ReportedAt = reported.Time
		}
		recipients = append(recipients, recipient)
	}
	return recipients, rows.Err()
}

// DeliveryStatusForMessage resolves the delivery detail for an opened message:
// an inbound correlated report, or the sender's own Sent message. A zero view
// (Kind == "") means the message has nothing DSN-related to show.
func (db *DB) DeliveryStatusForMessage(ctx context.Context, userID string, localMessageID int64) (DeliveryStatusView, error) {
	// An inbound report this account received.
	var correlation, reportSendID string
	err := db.Read().QueryRowContext(ctx, `
		SELECT r.correlation, COALESCE(r.send_id, '')
		FROM outgoing_dsn_reports r
		JOIN messages m ON m.id = r.message_local_id
		JOIN accounts a ON a.id = m.account_id
		WHERE r.message_local_id = ? AND a.user_id = ?`, localMessageID, userID).Scan(&correlation, &reportSendID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DeliveryStatusView{}, err
	}
	if err == nil && correlation == dsnMarkerCorrelated && reportSendID != "" {
		recipients, err := db.DSNRecipientsForSend(ctx, reportSendID)
		if err != nil {
			return DeliveryStatusView{}, err
		}
		var state string
		_ = db.Read().QueryRowContext(ctx, `SELECT dsn_state FROM outgoing_sends WHERE id = ?`, reportSendID).Scan(&state)
		return DeliveryStatusView{Kind: "report", SendID: reportSendID, DSNState: state, Recipients: recipients}, nil
	}

	// The sender's own Sent message. The draft row disappears after delivery,
	// so anchor on the Sent copy's Internet Message-ID; fall back to the
	// still-present draft link used by scheduled/pending sends.
	var internetID string
	err = db.Read().QueryRowContext(ctx, `
		SELECT COALESCE(internet_message_id, '') FROM messages WHERE id = ?`, localMessageID).Scan(&internetID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryStatusView{}, nil
	}
	if err != nil {
		return DeliveryStatusView{}, err
	}
	var sendID, state string
	query := `
		SELECT os.id, os.dsn_state
		FROM outgoing_sends os
		JOIN accounts a ON a.id = os.account_id
		WHERE a.user_id = ? AND os.dsn_state != '' AND (
			(NULLIF(?, '') IS NOT NULL AND os.sent_message_id = ?)
			OR os.message_id = ?
		)
		ORDER BY os.created_at DESC LIMIT 1`
	err = db.Read().QueryRowContext(ctx, query, userID, internetID, internetID, localMessageID).Scan(&sendID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryStatusView{}, nil
	}
	if err != nil {
		return DeliveryStatusView{}, err
	}
	recipients, err := db.DSNRecipientsForSend(ctx, sendID)
	if err != nil {
		return DeliveryStatusView{}, err
	}
	return DeliveryStatusView{Kind: "sent", SendID: sendID, DSNState: state, Recipients: recipients}, nil
}

// normalizeDSNAddress lowercases and trims an envelope address for matching.
func normalizeDSNAddress(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "<> \t"))
	if value == "" {
		return ""
	}
	return strings.ToLower(value)
}

func normalizeEnvelopeID(value string) string {
	return strings.TrimSpace(value)
}

func truncateDSNText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
