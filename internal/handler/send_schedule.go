package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/recurrence"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	"github.com/cristianadrielbraun/gofer/internal/store"
	"github.com/google/uuid"
)

const (
	scheduleRepeatNone   = "none"
	scheduleRepeatDaily  = "daily"
	scheduleRepeatWeekly = "weekly"
)

// parseScheduleClock validates the hour/minute dropdown values: minutes stay
// on the same five-minute grid the single-schedule picker uses.
func parseScheduleClock(hourValue, minuteValue string) (int, int, error) {
	hour, err := strconv.Atoi(strings.TrimSpace(hourValue))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, errors.New("use a valid schedule time")
	}
	minute, err := strconv.Atoi(strings.TrimSpace(minuteValue))
	if err != nil || minute < 0 || minute > 59 || minute%5 != 0 {
		return 0, 0, errors.New("use a valid schedule time")
	}
	return hour, minute, nil
}

// parseScheduleWeekdays accepts a comma-separated list of JS weekday numbers
// (0=Sunday..6=Saturday), returning the set and its stored encoding.
func parseScheduleWeekdays(raw string) ([]time.Weekday, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", errors.New("choose at least one weekday")
	}
	seen := map[time.Weekday]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n > 6 {
			return nil, "", errors.New("use a valid weekday")
		}
		seen[time.Weekday(n)] = true
	}
	if len(seen) == 0 {
		return nil, "", errors.New("choose at least one weekday")
	}
	var weekdays []time.Weekday
	for day := time.Sunday; day <= time.Saturday; day++ {
		if seen[day] {
			weekdays = append(weekdays, day)
		}
	}
	return weekdays, recurrence.EncodeWeekdays(weekdays), nil
}

// scheduleRuleFromForm validates the recurring rule fields. startDate is the
// already-validated schedule_date value.
func scheduleRuleFromForm(r *http.Request, loc *time.Location, startDate string) (recurrence.Rule, string, error) {
	repeat := strings.TrimSpace(r.FormValue("schedule_repeat"))
	if repeat != scheduleRepeatDaily && repeat != scheduleRepeatWeekly {
		return recurrence.Rule{}, "", errors.New("choose how the schedule repeats")
	}
	hour, minute, err := parseScheduleClock(r.FormValue("schedule_hour"), r.FormValue("schedule_minute"))
	if err != nil {
		return recurrence.Rule{}, "", err
	}
	rule := recurrence.Rule{
		Recurrence: recurrence.Recurrence(repeat),
		Timezone:   loc.String(),
		Hour:       hour,
		Minute:     minute,
	}
	rule.StartDate, err = recurrence.ParseCivilDate(startDate)
	if err != nil {
		return recurrence.Rule{}, "", err
	}
	if end := strings.TrimSpace(r.FormValue("schedule_end_date")); end != "" {
		if rule.EndDate, err = recurrence.ParseCivilDate(end); err != nil {
			return recurrence.Rule{}, "", err
		}
		if rule.EndDate.Before(rule.StartDate) {
			return recurrence.Rule{}, "", errors.New("the end date cannot be before the start date")
		}
	}
	if repeat == scheduleRepeatWeekly {
		weekdays, encoded, err := parseScheduleWeekdays(r.FormValue("schedule_weekdays"))
		if err != nil {
			return recurrence.Rule{}, "", err
		}
		rule.Weekdays = weekdays
		return rule, encoded, nil
	}
	if err := rule.Validate(); err != nil {
		return recurrence.Rule{}, "", err
	}
	return rule, "", nil
}

// createRecurringSchedule persists a daily/weekly plan from an already saved
// draft, copying attachments into plan-owned storage and then deleting the
// draft so later draft edits and deletes can never affect the plan.
func (h *Handler) createRecurringSchedule(r *http.Request, loc *time.Location, startDate string, saved composeDraftSaveResult, msg *message.OutgoingMessage) (storage.SendSchedule, *composeRequestError) {
	ctx := r.Context()
	rule, encodedWeekdays, err := scheduleRuleFromForm(r, loc, startDate)
	if err != nil {
		return storage.SendSchedule{}, &composeRequestError{status: http.StatusBadRequest, message: err.Error()}
	}

	planID := uuid.NewString()
	committed := false
	defer func() {
		if !committed {
			_ = h.blobStore.DeleteSchedule(saved.AccountID, planID)
		}
	}()

	// Recurring messages are independent new messages: no draft thread headers
	// and no frozen identity; each occurrence gets its own Message-ID and Date.
	msg.InReplyTo = ""
	msg.References = ""
	msg.MessageID = ""
	msg.Date = time.Time{}

	version, err := h.blobStore.NewScheduleVersion(saved.AccountID, planID)
	if err != nil {
		return storage.SendSchedule{}, &composeRequestError{status: http.StatusInternalServerError, message: "failed to save schedule attachments"}
	}
	for i, attachment := range msg.Attachments {
		path, copyErr := stageScheduleAttachment(ctx, version, saved.AccountID, planID, i+1, attachment)
		if copyErr != nil {
			return storage.SendSchedule{}, &composeRequestError{status: http.StatusInternalServerError, message: "failed to save schedule attachments"}
		}
		msg.Attachments[i].Path = path
	}
	contentJSON, err := json.Marshal(snapshotOutgoingMessage(msg))
	if err != nil {
		return storage.SendSchedule{}, &composeRequestError{status: http.StatusInternalServerError, message: "failed to save schedule"}
	}
	plan, err := h.db.CreateSendSchedule(ctx, storage.CreateSendScheduleInput{
		ID:          planID,
		AccountID:   saved.AccountID,
		Recurrence:  string(rule.Recurrence),
		Timezone:    rule.Timezone,
		Hour:        rule.Hour,
		Minute:      rule.Minute,
		StartDate:   rule.StartDate.String(),
		EndDate:     endDateValue(rule),
		Weekdays:    encodedWeekdays,
		ContentJSON: contentJSON,
	})
	if err != nil {
		return storage.SendSchedule{}, &composeRequestError{status: http.StatusBadRequest, message: err.Error()}
	}

	// The plan owns its content now; the creation draft is only staging. Its
	// discard queues the remote draft deletion and cancels nothing of the plan.
	if folder, discardErr := h.db.DiscardIMAPDraft(ctx, saved.AccountID, saved.DraftID); discardErr != nil {
		log.Printf("outgoing-schedule: discard source draft %s: %v", saved.DraftID, discardErr)
		if folder != "" {
			h.publishMutation(saved.AccountID, folder)
		}
	}
	committed = true
	h.wakeScheduleWorker(saved.AccountID)
	h.publishMutation(saved.AccountID, "scheduled")
	return plan, nil
}

func stageScheduleAttachment(ctx context.Context, version *store.BlobStore, accountID, planID string, attID int, attachment message.OutgoingAttachment) (string, error) {
	file, err := os.Open(attachment.Path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return version.StoreScheduleAttachment(ctx, accountID, planID, int64(attID), attachment.Filename, file)
}

func endDateValue(rule recurrence.Rule) string {
	if rule.EndDate.IsZero() {
		return ""
	}
	return rule.EndDate.String()
}

// scheduleSendBuilder renders an occurrence's independent delivery snapshot.
// It is pure local work, safe to run inside the materialization transaction.
func (h *Handler) scheduleSendBuilder(cfg *models.AccountConfig) storage.ScheduleSendBuilder {
	return func(_ string, contentJSON []byte, occurrence recurrence.Occurrence) (storage.PreparedScheduleSend, error) {
		var snapshot outgoingMessageSnapshot
		if err := json.Unmarshal(contentJSON, &snapshot); err != nil {
			return storage.PreparedScheduleSend{}, errors.New("stored schedule content is unreadable")
		}
		msg := snapshot.outgoingMessage()
		msg.MessageID = message.NewMessageID()
		msg.Date = occurrence.Instant
		recipients := message.AllRecipients(msg)
		if len(recipients) == 0 {
			return storage.PreparedScheduleSend{}, errors.New("scheduled message has no recipient")
		}
		transport := outgoingTransportForMessage(cfg, msg)
		raw, err := buildOutgoingMIME(transport, msg)
		if err != nil {
			return storage.PreparedScheduleSend{}, err
		}
		encoded, err := json.Marshal(snapshotOutgoingMessage(msg))
		if err != nil {
			return storage.PreparedScheduleSend{}, err
		}
		return storage.PreparedScheduleSend{
			Transport:          transport,
			EnvelopeFrom:       msg.FromEmail,
			EnvelopeRecipients: recipients,
			MIMEData:           raw,
			MessageJSON:        encoded,
		}, nil
	}
}

func (h *Handler) wakeScheduleWorker(accountID string) {
	if h.userMutationState != nil {
		h.userMutationState.wake[accountID] = true
		return
	}
	h.signalOutgoingWorker()
}

// --- JSON responses ---------------------------------------------------------

type sendScheduleResponse struct {
	ID               string `json:"id"`
	AccountID        string `json:"account_id"`
	Status           string `json:"status"`
	Recurrence       string `json:"recurrence"`
	Timezone         string `json:"timezone"`
	Hour             int    `json:"hour"`
	Minute           int    `json:"minute"`
	StartDate        string `json:"start_date"`
	EndDate          string `json:"end_date,omitempty"`
	Weekdays         []int  `json:"weekdays"`
	Subject          string `json:"subject,omitempty"`
	NextOccurrenceAt string `json:"next_occurrence_at,omitempty"`
	UnfinishedCount  int    `json:"unfinished_count"`
	TotalInstances   int    `json:"total_instances"`
	EndedReason      string `json:"ended_reason,omitempty"`
	CreatedAt        string `json:"created_at"`
}

type sendScheduleContentResponse struct {
	sendScheduleResponse
	FromName     string                       `json:"from_name,omitempty"`
	FromEmail    string                       `json:"from_email"`
	To           []outgoingAddressSnapshot    `json:"to"`
	CC           []outgoingAddressSnapshot    `json:"cc"`
	BCC          []outgoingAddressSnapshot    `json:"bcc"`
	TextBody     string                       `json:"text_body"`
	HTMLBody     string                       `json:"html_body"`
	Attachments  []outgoingAttachmentSnapshot `json:"attachments"`
	IdentityID   string                       `json:"identity_id,omitempty"`
	IdentitySMTP bool                         `json:"identity_smtp"`
}

type sendScheduleInstanceResponse struct {
	ID             string `json:"id"`
	LocalDate      string `json:"local_date"`
	ScheduledAt    string `json:"scheduled_at"`
	Status         string `json:"status"`
	SkipReason     string `json:"skip_reason,omitempty"`
	OutgoingSendID string `json:"outgoing_send_id,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	AttemptCount   int    `json:"attempt_count"`
	SentAt         string `json:"sent_at,omitempty"`
}

func weekdaysResponse(encoded string) []int {
	days, err := recurrence.ParseWeekdays(encoded)
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(days))
	for _, day := range days {
		out = append(out, int(day))
	}
	return out
}

func (h *Handler) scheduleResponse(plan storage.SendSchedule, unfinished, total int) sendScheduleResponse {
	resp := sendScheduleResponse{
		ID: plan.ID, AccountID: plan.AccountID, Status: plan.Status,
		Recurrence: plan.Recurrence, Timezone: plan.Timezone,
		Hour: plan.Hour, Minute: plan.Minute, StartDate: plan.StartDate,
		EndDate: plan.EndDate, Weekdays: weekdaysResponse(plan.Weekdays),
		UnfinishedCount: unfinished, TotalInstances: total,
		EndedReason: plan.EndedReason,
		CreatedAt:   outgoingSendTime(plan.CreatedAt),
	}
	if plan.HasNextOccurrence {
		resp.NextOccurrenceAt = outgoingSendTime(plan.NextOccurrenceAt)
	}
	if len(plan.ContentJSON) > 0 {
		var snapshot outgoingMessageSnapshot
		if json.Unmarshal(plan.ContentJSON, &snapshot) == nil {
			resp.Subject = snapshot.Subject
		}
	}
	return resp
}

func (h *Handler) writeScheduleJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// scheduleAPIError answers a plan API error without the ambiguous-warning
// flag ordinary outgoing delivery errors carry.
func scheduleAPIError(w http.ResponseWriter, status int, message string) {
	writeOutgoingSendError(w, status, message, false)
}

func (h *Handler) loadOwnedSchedule(w http.ResponseWriter, r *http.Request) (storage.SendSchedule, bool) {
	plan, err := h.db.GetSendScheduleForUser(r.Context(), h.userID(r.Context()), r.PathValue("id"))
	if errors.Is(err, storage.ErrSendScheduleNotFound) {
		scheduleAPIError(w, http.StatusNotFound, "Send schedule not found")
		return storage.SendSchedule{}, false
	}
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to load send schedule")
		return storage.SendSchedule{}, false
	}
	return plan, true
}

func (h *Handler) handleSendScheduleList(w http.ResponseWriter, r *http.Request) {
	plans, err := h.db.ListSendSchedulesForUser(r.Context(), h.userID(r.Context()))
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to load send schedules")
		return
	}
	out := make([]sendScheduleResponse, 0, len(plans))
	for _, plan := range plans {
		out = append(out, h.scheduleResponse(plan.SendSchedule, plan.UnfinishedCount, plan.TotalInstances))
	}
	h.writeScheduleJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (h *Handler) handleSendScheduleGet(w http.ResponseWriter, r *http.Request) {
	plan, ok := h.loadOwnedSchedule(w, r)
	if !ok {
		return
	}
	instances, err := h.db.GetSendScheduleInstancesForUser(r.Context(), h.userID(r.Context()), plan.ID, 200)
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to load schedule instances")
		return
	}
	unfinished := 0
	for _, instance := range instances {
		switch instance.Status {
		case storage.SendScheduleInstancePending, storage.SendScheduleInstanceSending,
			storage.SendScheduleInstanceFailed, storage.SendScheduleInstanceAmbiguous:
			unfinished++
		}
	}
	var snapshot outgoingMessageSnapshot
	_ = json.Unmarshal(plan.ContentJSON, &snapshot)
	resp := sendScheduleContentResponse{
		sendScheduleResponse: h.scheduleResponse(plan, unfinished, len(instances)),
		FromName:              snapshot.FromName,
		FromEmail:             snapshot.FromEmail,
		To:                    snapshot.To,
		CC:                    snapshot.CC,
		BCC:                   snapshot.BCC,
		TextBody:              snapshot.TextBody,
		HTMLBody:              snapshot.HTMLBody,
		Attachments:           snapshot.Attachments,
		IdentityID:            snapshot.IdentityID,
		IdentitySMTP:          snapshot.IdentitySMTP,
	}
	h.writeScheduleJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSendScheduleInstances(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.loadOwnedSchedule(w, r); !ok {
		return
	}
	instances, err := h.db.GetSendScheduleInstancesForUser(r.Context(), h.userID(r.Context()), r.PathValue("id"), 200)
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to load schedule instances")
		return
	}
	out := make([]sendScheduleInstanceResponse, 0, len(instances))
	for _, instance := range instances {
		item := sendScheduleInstanceResponse{
			ID: instance.ID, LocalDate: instance.LocalDate,
			ScheduledAt: outgoingSendTime(instance.ScheduledAt),
			Status: instance.Status, SkipReason: instance.SkipReason,
			OutgoingSendID: instance.OutgoingSendID, LastError: instance.LastError,
			AttemptCount: instance.AttemptCount,
		}
		if !instance.SentAt.IsZero() {
			item.SentAt = outgoingSendTime(instance.SentAt)
		}
		out = append(out, item)
	}
	h.writeScheduleJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func (h *Handler) handleSendScheduleSettings(w http.ResponseWriter, r *http.Request) {
	plan, ok := h.loadOwnedSchedule(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, "invalid form data")
		return
	}
	loc, err := h.scheduleLocation(r.Context(), h.userID(r.Context()), r.FormValue("schedule_timezone"))
	if err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	hour, minute, err := parseScheduleClock(r.FormValue("schedule_hour"), r.FormValue("schedule_minute"))
	if err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	repeat := strings.TrimSpace(r.FormValue("schedule_repeat"))
	if repeat != scheduleRepeatDaily && repeat != scheduleRepeatWeekly {
		writeComposeJSONError(w, http.StatusBadRequest, "choose how the schedule repeats")
		return
	}
	var weekdays []time.Weekday
	encodedWeekdays := ""
	if repeat == scheduleRepeatWeekly {
		weekdays, encodedWeekdays, err = parseScheduleWeekdays(r.FormValue("schedule_weekdays"))
		if err != nil {
			writeComposeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	input := storage.UpdateSendScheduleSettingsInput{
		Recurrence: repeat, Timezone: loc.String(), Hour: hour, Minute: minute,
		EndDate: strings.TrimSpace(r.FormValue("schedule_end_date")), Weekdays: encodedWeekdays,
	}
	if start := strings.TrimSpace(r.FormValue("schedule_start_date")); start != "" {
		if _, err := recurrence.ParseCivilDate(start); err != nil {
			writeComposeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.StartDate = start
	}
	// Validate with the pure engine before touching the plan.
	startDate := plan.StartDate
	if input.StartDate != "" {
		startDate = input.StartDate
	}
	startCivil, err := recurrence.ParseCivilDate(startDate)
	if err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule := recurrence.Rule{
		Recurrence: recurrence.Recurrence(repeat), Timezone: loc.String(),
		Hour: hour, Minute: minute, StartDate: startCivil, Weekdays: weekdays,
	}
	if input.EndDate != "" {
		if rule.EndDate, err = recurrence.ParseCivilDate(input.EndDate); err != nil {
			writeComposeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := rule.Validate(); err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.db.UpdateSendScheduleSettingsForUser(r.Context(), h.userID(r.Context()), plan.ID, input)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrSendScheduleNotActive) {
			status = http.StatusConflict
		}
		writeComposeJSONError(w, status, err.Error())
		return
	}
	h.wakeScheduleWorker(plan.AccountID)
	h.publishMutation(plan.AccountID, "scheduled")
	h.writeScheduleJSON(w, http.StatusOK, h.scheduleResponse(updated, 0, 0))
}

func (h *Handler) handleSendScheduleAction(w http.ResponseWriter, r *http.Request) {
	plan, ok := h.loadOwnedSchedule(w, r)
	if !ok {
		return
	}
	userID := h.userID(r.Context())
	var (
		updated storage.SendSchedule
		err     error
	)
	switch r.PathValue("action") {
	case "pause":
		updated, err = h.db.PauseSendScheduleForUser(r.Context(), userID, plan.ID)
	case "resume":
		updated, err = h.db.ResumeSendScheduleForUser(r.Context(), userID, plan.ID, time.Now().UTC())
	case "end":
		updated, err = h.db.EndSendScheduleForUser(r.Context(), userID, plan.ID, "user")
		if err == nil {
			// Best-effort immediate release; regular cleanup covers races.
			instances, listErr := h.db.GetSendScheduleInstancesForUser(r.Context(), userID, plan.ID, 200)
			if listErr == nil && countUnfinishedInstances(instances) == 0 {
				_ = h.blobStore.DeleteSchedule(plan.AccountID, plan.ID)
			}
		}
	default:
		scheduleAPIError(w, http.StatusBadRequest, "Unknown schedule action")
		return
	}
	if err != nil {
		writeComposeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	h.wakeScheduleWorker(plan.AccountID)
	h.publishMutation(plan.AccountID, "scheduled")
	h.writeScheduleJSON(w, http.StatusOK, h.scheduleResponse(updated, 0, 0))
}

func countUnfinishedInstances(instances []storage.SendScheduleInstance) int {
	n := 0
	for _, instance := range instances {
		switch instance.Status {
		case storage.SendScheduleInstancePending, storage.SendScheduleInstanceSending,
			storage.SendScheduleInstanceFailed, storage.SendScheduleInstanceAmbiguous:
			n++
		}
	}
	return n
}

// handleSendScheduleContent replaces the editable future content. Existing
// attachments are carried forward unless their index is listed in
// schedule_attachment_remove, and new multipart attachments are appended.
func (h *Handler) handleSendScheduleContent(w http.ResponseWriter, r *http.Request) {
	plan, ok := h.loadOwnedSchedule(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, "invalid form data")
		return
	}
	ctx := r.Context()
	if toAddrs, err := message.ParseAddressList(r.FormValue("to")); err != nil || len(toAddrs) == 0 {
		writeComposeJSONError(w, http.StatusBadRequest, "Please enter at least one recipient.")
		return
	}
	account, err := h.ownedAccount(ctx, plan.AccountID)
	if err != nil || account == nil {
		writeComposeJSONError(w, http.StatusNotFound, "account not found")
		return
	}
	sender, err := h.composeSenderFor(ctx, account, r, true)
	if err != nil {
		status, text := composeSenderError(err)
		writeComposeJSONError(w, status, text)
		return
	}

	var existing outgoingMessageSnapshot
	if err := json.Unmarshal(plan.ContentJSON, &existing); err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "stored schedule content is unreadable")
		return
	}
	removed := parseRemovedAttachmentIndices(r.FormValue("schedule_attachment_remove"))
	msg := &message.OutgoingMessage{
		To:          mustAddressList(r.FormValue("to")),
		CC:          mustAddressList(r.FormValue("cc")),
		Bcc:         mustAddressList(r.FormValue("bcc")),
		Subject:     r.FormValue("subject"),
		TextBody:    r.FormValue("body"),
		Attachments: []message.OutgoingAttachment{},
	}
	if html := strings.TrimSpace(r.FormValue("html_body")); html != "" {
		msg.HTMLBody = string(message.SanitizeHTML([]byte(html)))
	}
	sender.apply(msg)

	version, err := h.blobStore.NewScheduleVersion(plan.AccountID, plan.ID)
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to save schedule attachments")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = h.blobStore.DeleteScheduleVersion(plan.AccountID, plan.ID, version.VersionNonce())
		}
	}()
	for i, attachment := range restoreOutgoingAttachments(existing.Attachments) {
		if removed[i] {
			continue
		}
		path, stageErr := stageScheduleAttachment(ctx, version, plan.AccountID, plan.ID, len(msg.Attachments)+1, attachment)
		if stageErr != nil {
			scheduleAPIError(w, http.StatusInternalServerError, "failed to save schedule attachments")
			return
		}
		attachment.Path = path
		msg.Attachments = append(msg.Attachments, attachment)
	}
	newAttachments, _, err := h.collectComposeAttachments(r)
	if err != nil {
		writeComposeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	for _, attachment := range newAttachments {
		path, stageErr := stageScheduleAttachment(ctx, version, plan.AccountID, plan.ID, len(msg.Attachments)+1, attachment)
		if stageErr != nil {
			scheduleAPIError(w, http.StatusInternalServerError, "failed to save schedule attachments")
			return
		}
		attachment.Path = path
		msg.Attachments = append(msg.Attachments, attachment)
	}
	if err := validateComposeMessageSize(msg.Attachments, msg.TextBody, msg.HTMLBody); err != nil {
		writeComposeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	contentJSON, err := json.Marshal(snapshotOutgoingMessage(msg))
	if err != nil {
		scheduleAPIError(w, http.StatusInternalServerError, "failed to save schedule")
		return
	}
	updated, err := h.db.UpdateSendScheduleContentForUser(ctx, h.userID(ctx), plan.ID, contentJSON)
	if err != nil {
		writeComposeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	committed = true
	if err := h.blobStore.DeleteScheduleVersionsExcept(plan.AccountID, plan.ID, version.VersionNonce()); err != nil {
		log.Printf("outgoing-schedule: prune old content versions %s: %v", plan.ID, err)
	}
	h.wakeScheduleWorker(plan.AccountID)
	h.publishMutation(plan.AccountID, "scheduled")
	h.writeScheduleJSON(w, http.StatusOK, h.scheduleResponse(updated, 0, 0))
}

func parseRemovedAttachmentIndices(raw string) map[int]bool {
	out := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil && n >= 0 {
			out[n] = true
		}
	}
	return out
}

func mustAddressList(raw string) []*mail.Address {
	addrs, _ := message.ParseAddressList(raw)
	return addrs
}
