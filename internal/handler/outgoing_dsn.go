package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/cristianadrielbraun/gofer/internal/mail/dsn"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	"github.com/cristianadrielbraun/gofer/internal/store"
)

// formRequestsDSN reports whether compose explicitly opted in to delivery
// status notifications. An absent or unchecked control means no request.
func formRequestsDSN(r interface{ FormValue(string) string }) bool {
	switch strings.ToLower(strings.TrimSpace(r.FormValue("request_dsn"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// processOpenedDeliveryReport correlates a message opened through the normal
// reading entry with the send its DSN answers. It never changes the original
// message, never resends anything, and only writes when the full report is
// available locally: a body/raw download failure leaves every stored result
// untouched so reopening the cached message can complete the work later.
func (h *Handler) processOpenedDeliveryReport(ctx context.Context, db *storage.DB, owner string, localID int64, accountID, rawPath string) {
	if db == nil {
		db = h.db
	}
	if db == nil || localID <= 0 || accountID == "" || strings.TrimSpace(rawPath) == "" {
		return
	}
	// Already examined on an earlier open: the marker decides, no file IO.
	var correlation string
	switch err := db.Read().QueryRowContext(ctx, `SELECT correlation FROM outgoing_dsn_reports WHERE message_local_id = ?`, localID).Scan(&correlation); {
	case err == nil:
		return
	case !errors.Is(err, sql.ErrNoRows):
		return
	}

	file, err := store.OpenMessageFile(rawPath)
	if err != nil {
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 35<<20+1))
	_ = file.Close()
	if readErr != nil || len(raw) > 35<<20 {
		return
	}
	report, isReport, err := dsn.ParseReport(raw)
	if err != nil || !isReport || report == nil {
		if !isReport {
			if markErr := db.MarkMessageNotDeliveryReport(ctx, accountID, localID); markErr != nil {
				log.Printf("outgoing-dsn: mark message %d not a report: %v", localID, markErr)
			}
		}
		return
	}
	input := storage.DSNReportInput{
		ReportKey:  dsn.ReportKey(report),
		MessageID:  strings.TrimSpace(report.MessageID),
		EnvelopeID: report.EnvelopeID,
		Recipients: make([]storage.DSNReportRecipientInput, 0, len(report.Recipients)),
	}
	for _, record := range report.Recipients {
		address := record.FinalRecipient
		if strings.TrimSpace(address) == "" {
			address = record.OriginalRecipient
		}
		input.Recipients = append(input.Recipients, storage.DSNReportRecipientInput{
			Address:    address,
			Action:     record.Action,
			StatusCode: record.StatusCode,
			Diagnostic: record.Diagnostic,
			ReportedAt: record.LastAttempt,
			Valid:      dsnRecordValid(address, record.Action),
		})
	}
	outcome, err := db.ProcessIncomingDeliveryReport(ctx, accountID, localID, input)
	if err != nil {
		// The commit failed on purpose or by accident; reopening repeats all
		// unconfirmed work without touching or resending the original mail.
		log.Printf("outgoing-dsn: process report for message %d: %v", localID, err)
		return
	}
	if outcome.Applied > 0 {
		h.publishMutation(accountID, "sent")
	}
}

func dsnRecordValid(address, action string) bool {
	address = strings.TrimSpace(strings.Trim(address, "<>"))
	if !strings.Contains(address, "@") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "delivered", "failed", "delayed", "relayed", "expanded":
		return true
	default:
		return false
	}
}

type deliveryStatusRecipientResponse struct {
	Address    string `json:"address"`
	State      string `json:"state"`
	StatusCode string `json:"status_code,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
	ReportedAt string `json:"reported_at,omitempty"`
}

type deliveryStatusResponse struct {
	Kind       string                            `json:"kind"`
	SendID     string                            `json:"send_id,omitempty"`
	DSNState   string                            `json:"dsn_state,omitempty"`
	Recipients []deliveryStatusRecipientResponse `json:"recipients,omitempty"`
}

// handleMessageDeliveryStatus answers the delivery detail an opened message
// carries: the correlated report of an inbound DSN, or the per-recipient
// status of the sender's own Sent message. Empty JSON means there is nothing
// DSN-related to show.
func (h *Handler) handleMessageDeliveryStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner := h.userID(ctx)
	localID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || localID <= 0 {
		http.NotFound(w, r)
		return
	}
	var view storage.DeliveryStatusView
	load := func(db *storage.DB) error {
		var err error
		view, err = db.DeliveryStatusForMessage(ctx, owner, localID)
		return err
	}
	if h.userStorage != nil {
		if err := h.withUserDB(ctx, owner, load); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusServiceUnavailable)
			return
		}
	} else {
		view, err = h.db.DeliveryStatusForMessage(ctx, owner, localID)
		if err != nil {
			http.Error(w, "failed to load delivery status", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if view.Kind == "" {
		_ = json.NewEncoder(w).Encode(deliveryStatusResponse{})
		return
	}
	response := deliveryStatusResponse{Kind: view.Kind, SendID: view.SendID, DSNState: view.DSNState}
	for _, recipient := range view.Recipients {
		item := deliveryStatusRecipientResponse{
			Address:    recipient.Address,
			State:      recipient.State,
			StatusCode: recipient.StatusCode,
			Diagnostic: recipient.Diagnostic,
		}
		if !recipient.ReportedAt.IsZero() {
			item.ReportedAt = recipient.ReportedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		response.Recipients = append(response.Recipients, item)
	}
	_ = json.NewEncoder(w).Encode(response)
}
