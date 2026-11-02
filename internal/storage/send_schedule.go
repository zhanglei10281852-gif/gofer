package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/recurrence"
	"github.com/google/uuid"
)

const (
	SendScheduleActive = "active"
	SendSchedulePaused = "paused"
	SendScheduleEnded  = "ended"
)

const (
	SendScheduleInstancePending   = "pending"
	SendScheduleInstanceSending   = "sending"
	SendScheduleInstanceSent      = "sent"
	SendScheduleInstanceFailed    = "failed"
	SendScheduleInstanceAmbiguous = "ambiguous"
	SendScheduleInstanceCanceled  = "canceled"
	SendScheduleInstanceSkipped   = "skipped"
)

const (
	SendScheduleSkipDstGap = "dst_gap"
	SendScheduleSkipMissed = "missed"
	SendScheduleSkipPaused = "paused"
)

// maxScheduleOccurrencesPerTick bounds a single materialization pass. It
// covers well over a year of daily downtime; beyond it the plan advances
// without freezing an occurrence rather than risk sending a stale one.
const maxScheduleOccurrencesPerTick = 500

var (
	ErrSendScheduleNotFound     = errors.New("send schedule not found")
	ErrSendScheduleNotPaused    = errors.New("send schedule is not paused")
	ErrSendScheduleNotActive    = errors.New("send schedule cannot be updated after it ended")
	ErrSendScheduleInvalidState = errors.New("send schedule is not in a valid state for this action")
)

// SendSchedule is a recurring delivery plan.
type SendSchedule struct {
	ID                string
	AccountID         string
	Status            string
	Recurrence        string
	Timezone          string
	Hour              int
	Minute            int
	StartDate         string
	EndDate           string
	Weekdays          string
	ContentJSON       []byte
	CursorDate        string
	NextOccurrenceAt  time.Time
	HasNextOccurrence bool
	EndedReason       string
	PausedAt          time.Time
	EndedAt           time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// SendScheduleSummary is the user-facing plan row with instance counters.
type SendScheduleSummary struct {
	SendSchedule
	UnfinishedCount int
	TotalInstances  int
}

// SendScheduleInstance is one local date's independent delivery record.
type SendScheduleInstance struct {
	ID             string
	ScheduleID     string
	LocalDate      string
	ScheduledAt    time.Time
	Status         string
	SkipReason     string
	OutgoingSendID string
	LastError      string
	AttemptCount   int
	SentAt         time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateSendScheduleInput carries the validated plan fields. A blank ID lets
// the store generate one; callers that stage files first supply their own.
type CreateSendScheduleInput struct {
	ID          string
	AccountID   string
	Recurrence  string
	Timezone    string
	Hour        int
	Minute      int
	StartDate   string
	EndDate     string
	Weekdays    string
	ContentJSON []byte
}

// UpdateSendScheduleSettingsInput carries editable rule fields.
type UpdateSendScheduleSettingsInput struct {
	Recurrence string
	Timezone   string
	Hour       int
	Minute     int
	StartDate  string
	EndDate    string
	Weekdays   string
}

func scanSendSchedule(row rowScanner) (SendSchedule, error) {
	var s SendSchedule
	var next sql.NullTime
	var pausedAt, endedAt, createdAt, updatedAt sqliteNullTime
	if err := row.Scan(
		&s.ID, &s.AccountID, &s.Status, &s.Recurrence, &s.Timezone,
		&s.Hour, &s.Minute, &s.StartDate, &s.EndDate, &s.Weekdays,
		&s.ContentJSON, &s.CursorDate, &next, &s.EndedReason,
		&pausedAt, &endedAt, &createdAt, &updatedAt,
	); err != nil {
		return SendSchedule{}, err
	}
	s.HasNextOccurrence = next.Valid
	if next.Valid {
		s.NextOccurrenceAt = next.Time.UTC()
	}
	if pausedAt.Valid {
		s.PausedAt = pausedAt.Time
	}
	if endedAt.Valid {
		s.EndedAt = endedAt.Time
	}
	if createdAt.Valid {
		s.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		s.UpdatedAt = updatedAt.Time
	}
	return s, nil
}

const sendScheduleSelect = `SELECT s.id, s.account_id, s.status, s.recurrence, s.timezone, s.hour, s.minute,
	s.start_date, s.end_date, s.weekdays, s.content_json, s.cursor_date, s.next_occurrence_at, s.ended_reason,
	s.paused_at, s.ended_at, s.created_at, s.updated_at
	FROM send_schedules s`

// rule builds the pure recurrence rule for a stored plan.
func (s SendSchedule) rule() (recurrence.Rule, error) {
	start, err := recurrence.ParseCivilDate(s.StartDate)
	if err != nil {
		return recurrence.Rule{}, err
	}
	rule := recurrence.Rule{
		Recurrence: recurrence.Recurrence(s.Recurrence),
		Timezone:   s.Timezone,
		Hour:       s.Hour,
		Minute:     s.Minute,
		StartDate:  start,
	}
	if s.EndDate != "" {
		end, err := recurrence.ParseCivilDate(s.EndDate)
		if err != nil {
			return recurrence.Rule{}, err
		}
		rule.EndDate = end
	}
	if s.Recurrence == string(recurrence.Weekly) {
		weekdays, err := recurrence.ParseWeekdays(s.Weekdays)
		if err != nil {
			return recurrence.Rule{}, err
		}
		rule.Weekdays = weekdays
	}
	return rule, nil
}

func (s SendSchedule) location() (*time.Location, error) {
	return time.LoadLocation(s.Timezone)
}

// nextFutureOccurrence returns the first occurrence at or after cursorDate
// whose instant is still in the future; gap and already-passed occurrences
// are skipped over.
func nextFutureOccurrence(rule recurrence.Rule, cursorDate string, now time.Time, loc *time.Location) (time.Time, bool) {
	cursor, err := recurrence.ParseCivilDate(cursorDate)
	if err != nil {
		cursor = rule.StartDate
	}
	for range maxScheduleOccurrencesPerTick {
		occ, ok := rule.NextOccurrence(cursor, loc)
		if !ok {
			return time.Time{}, false
		}
		if !occ.Gap && occ.Instant.After(now) {
			return occ.Instant.UTC(), true
		}
		cursor = occ.Date.AddDays(1)
	}
	return time.Time{}, false
}

func nullableInstant(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (db *DB) CreateSendSchedule(ctx context.Context, input CreateSendScheduleInput) (SendSchedule, error) {
	now := time.Now().UTC()
	planID := input.ID
	if planID == "" {
		planID = uuid.NewString()
	}
	plan := SendSchedule{
		ID:          planID,
		AccountID:   input.AccountID,
		Status:      SendScheduleActive,
		Recurrence:  input.Recurrence,
		Timezone:    input.Timezone,
		Hour:        input.Hour,
		Minute:      input.Minute,
		StartDate:   input.StartDate,
		EndDate:     input.EndDate,
		Weekdays:    input.Weekdays,
		ContentJSON: append([]byte(nil), input.ContentJSON...),
		CursorDate:  input.StartDate,
	}
	rule, err := plan.rule()
	if err != nil {
		return SendSchedule{}, err
	}
	if err := rule.Validate(); err != nil {
		return SendSchedule{}, err
	}
	loc, err := plan.location()
	if err != nil {
		return SendSchedule{}, err
	}
	next, hasNext := nextFutureOccurrence(rule, plan.CursorDate, now, loc)
	if _, err := db.Write().ExecContext(ctx, `
		INSERT INTO send_schedules (
			id, account_id, status, recurrence, timezone, hour, minute,
			start_date, end_date, weekdays, content_json, cursor_date, next_occurrence_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.AccountID, plan.Status, plan.Recurrence, plan.Timezone,
		plan.Hour, plan.Minute, plan.StartDate, plan.EndDate, plan.Weekdays,
		string(plan.ContentJSON), plan.CursorDate, nullableInstant(next),
	); err != nil {
		return SendSchedule{}, fmt.Errorf("create send schedule: %w", err)
	}
	if !hasNext {
		return SendSchedule{}, fmt.Errorf("schedule has no future occurrence")
	}
	return db.GetSendScheduleForAccount(ctx, plan.AccountID, plan.ID)
}

// GetSendScheduleForUser loads a plan owned by userID.
func (db *DB) GetSendScheduleForUser(ctx context.Context, userID, scheduleID string) (SendSchedule, error) {
	plan, err := scanSendSchedule(db.Read().QueryRowContext(ctx, sendScheduleSelect+`
		JOIN accounts a ON a.id = s.account_id
		WHERE s.id = ? AND a.user_id = ?`, scheduleID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return SendSchedule{}, ErrSendScheduleNotFound
	}
	return plan, err
}

// GetSendScheduleForAccount loads a plan on an account (internal worker path).
func (db *DB) GetSendScheduleForAccount(ctx context.Context, accountID, scheduleID string) (SendSchedule, error) {
	plan, err := scanSendSchedule(db.Read().QueryRowContext(ctx, sendScheduleSelect+`
		WHERE s.id = ? AND s.account_id = ?`, scheduleID, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return SendSchedule{}, ErrSendScheduleNotFound
	}
	return plan, err
}

// ListSendSchedulesForUser returns the owner's plans, active ones first.
func (db *DB) ListSendSchedulesForUser(ctx context.Context, userID string) ([]SendScheduleSummary, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT s.id, s.account_id, s.status, s.recurrence, s.timezone, s.hour AS hour, s.minute AS minute,
			s.start_date, s.end_date, s.weekdays, s.content_json, s.cursor_date,
			s.next_occurrence_at, s.ended_reason, s.paused_at, s.ended_at, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM send_schedule_instances i
			 WHERE i.schedule_id = s.id AND i.status IN ('pending','sending','failed','ambiguous')) AS unfinished,
			(SELECT COUNT(*) FROM send_schedule_instances i WHERE i.schedule_id = s.id) AS total
		FROM send_schedules s
		JOIN accounts a ON a.id = s.account_id
		WHERE a.user_id = ?
		ORDER BY (s.status = 'ended'), s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SendScheduleSummary
	for rows.Next() {
		var summary SendScheduleSummary
		var next sql.NullTime
		var pausedAt, endedAt, createdAt, updatedAt sqliteNullTime
		if err := rows.Scan(
			&summary.ID, &summary.AccountID, &summary.Status, &summary.Recurrence, &summary.Timezone,
			&summary.Hour, &summary.Minute, &summary.StartDate, &summary.EndDate, &summary.Weekdays,
			&summary.ContentJSON, &summary.CursorDate, &next, &summary.EndedReason,
			&pausedAt, &endedAt, &createdAt, &updatedAt,
			&summary.UnfinishedCount, &summary.TotalInstances,
		); err != nil {
			return nil, err
		}
		summary.HasNextOccurrence = next.Valid
		if next.Valid {
			summary.NextOccurrenceAt = next.Time.UTC()
		}
		if pausedAt.Valid {
			summary.PausedAt = pausedAt.Time
		}
		if endedAt.Valid {
			summary.EndedAt = endedAt.Time
		}
		if createdAt.Valid {
			summary.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			summary.UpdatedAt = updatedAt.Time
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

// GetSendScheduleInstancesForUser lists one plan's occurrences newest first.
func (db *DB) GetSendScheduleInstancesForUser(ctx context.Context, userID, scheduleID string, limit int) ([]SendScheduleInstance, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Read().QueryContext(ctx, `
		SELECT i.id, i.schedule_id, i.local_date, i.scheduled_at, i.status, i.skip_reason,
			i.outgoing_send_id, i.last_error, i.attempt_count, i.sent_at, i.created_at, i.updated_at
		FROM send_schedule_instances i
		JOIN send_schedules s ON s.id = i.schedule_id
		JOIN accounts a ON a.id = s.account_id
		WHERE a.user_id = ? AND i.schedule_id = ?
		ORDER BY i.local_date DESC, i.created_at DESC
		LIMIT ?`, userID, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SendScheduleInstance
	for rows.Next() {
		instance, err := scanSendScheduleInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, instance)
	}
	return out, rows.Err()
}

func scanSendScheduleInstance(row rowScanner) (SendScheduleInstance, error) {
	var i SendScheduleInstance
	var scheduledAt, sentAt, createdAt, updatedAt sqliteNullTime
	if err := row.Scan(
		&i.ID, &i.ScheduleID, &i.LocalDate, &scheduledAt, &i.Status, &i.SkipReason,
		&i.OutgoingSendID, &i.LastError, &i.AttemptCount, &sentAt, &createdAt, &updatedAt,
	); err != nil {
		return SendScheduleInstance{}, err
	}
	if scheduledAt.Valid {
		i.ScheduledAt = scheduledAt.Time
	}
	if sentAt.Valid {
		i.SentAt = sentAt.Time
	}
	if createdAt.Valid {
		i.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		i.UpdatedAt = updatedAt.Time
	}
	return i, nil
}

// ListAccountsWithActiveSchedules returns accounts that have an active or
// paused plan the worker must enumerate.
func (db *DB) ListAccountsWithActiveSchedules(ctx context.Context) ([]string, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT DISTINCT account_id FROM send_schedules
		WHERE status IN ('active','paused')
		ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		accounts = append(accounts, id)
	}
	return accounts, rows.Err()
}

// CountActiveSchedulesForUser counts schedules with a future occurrence, for
// the Scheduled folder badge.
func (db *DB) CountActiveSchedulesForUser(ctx context.Context, userID string) (int, error) {
	var count int
	err := db.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM send_schedules s JOIN accounts a ON a.id = s.account_id
		WHERE a.user_id = ? AND s.status IN ('active','paused') AND s.next_occurrence_at IS NOT NULL`, userID).Scan(&count)
	return count, err
}

// GetScheduledFolderCountForUser is the Scheduled folder badge: pending
// single-schedule drafts plus plans that still have a future occurrence.
func (db *DB) GetScheduledFolderCountForUser(ctx context.Context, userID string) (int, error) {
	var count int
	err := db.Read().QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*)
			 FROM outgoing_sends os
			 JOIN message_folder_state mfs ON mfs.message_id = os.message_id
			 JOIN messages m ON m.id = os.message_id
			 JOIN accounts a ON a.id = m.account_id
			 WHERE a.user_id = ? AND os.is_scheduled = 1 AND os.status = 'pending' AND mfs.is_deleted = 0)
			+
			(SELECT COUNT(*) FROM send_schedules s
			 JOIN accounts a ON a.id = s.account_id
			 WHERE a.user_id = ? AND s.status IN ('active','paused') AND s.next_occurrence_at IS NOT NULL)`,
		userID, userID).Scan(&count)
	return count, err
}

// ListAccountsForScheduleMaintenance lists accounts that need a worker pass:
// ones with active/paused plans, or ones whose ended plans still have
// unfinished instances to reconcile.
func (db *DB) ListAccountsForScheduleMaintenance(ctx context.Context) ([]string, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT DISTINCT s.account_id FROM send_schedules s
		WHERE s.status IN ('active','paused')
		   OR EXISTS (SELECT 1 FROM send_schedule_instances i
		              WHERE i.schedule_id=s.id
		                AND i.status IN ('pending','sending','failed','ambiguous'))
		ORDER BY s.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		accounts = append(accounts, id)
	}
	return accounts, rows.Err()
}

// UpdateSendScheduleSettingsForUser changes the rule of a plan that has not
// ended. Existing instances, including skipped dates, never change; the
// forward cursor decides which dates the new rule applies to.
func (db *DB) UpdateSendScheduleSettingsForUser(ctx context.Context, userID, scheduleID string, input UpdateSendScheduleSettingsInput) (SendSchedule, error) {
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return SendSchedule{}, err
	}
	defer tx.Rollback()
	plan, err := scanSendSchedule(tx.QueryRowContext(ctx, sendScheduleSelect+`
		JOIN accounts a ON a.id = s.account_id
		WHERE s.id = ? AND a.user_id = ?`, scheduleID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return SendSchedule{}, ErrSendScheduleNotFound
	}
	if err != nil {
		return SendSchedule{}, err
	}
	if plan.Status == SendScheduleEnded {
		return SendSchedule{}, ErrSendScheduleNotActive
	}
	updated := plan
	updated.Recurrence = input.Recurrence
	updated.Timezone = input.Timezone
	updated.Hour = input.Hour
	updated.Minute = input.Minute
	if input.StartDate != "" {
		updated.StartDate = input.StartDate
	}
	updated.EndDate = input.EndDate
	updated.Weekdays = input.Weekdays
	rule, err := updated.rule()
	if err != nil {
		return SendSchedule{}, err
	}
	if err := rule.Validate(); err != nil {
		return SendSchedule{}, err
	}
	loc, err := updated.location()
	if err != nil {
		return SendSchedule{}, err
	}
	now := time.Now().UTC()
	next, _ := nextFutureOccurrence(rule, plan.CursorDate, now, loc)
	if _, err := tx.ExecContext(ctx, `
		UPDATE send_schedules
		SET recurrence=?, timezone=?, hour=?, minute=?, start_date=?, end_date=?, weekdays=?,
			next_occurrence_at=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		updated.Recurrence, updated.Timezone, updated.Hour, updated.Minute,
		updated.StartDate, updated.EndDate, updated.Weekdays, nullableInstant(next), scheduleID,
	); err != nil {
		return SendSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return SendSchedule{}, err
	}
	return db.GetSendScheduleForAccount(ctx, plan.AccountID, scheduleID)
}

// UpdateSendScheduleContentForUser replaces the editable content template.
// Frozen instances keep the snapshot their own outgoing_sends row stores.
func (db *DB) UpdateSendScheduleContentForUser(ctx context.Context, userID, scheduleID string, contentJSON []byte) (SendSchedule, error) {
	if len(contentJSON) == 0 {
		return SendSchedule{}, fmt.Errorf("missing schedule content")
	}
	var accountID string
	err := db.Write().QueryRowContext(ctx, `
		UPDATE send_schedules SET content_json=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND account_id IN (SELECT id FROM accounts WHERE user_id=?)
		RETURNING account_id`, string(contentJSON), scheduleID, userID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return SendSchedule{}, ErrSendScheduleNotFound
	}
	if err != nil {
		return SendSchedule{}, err
	}
	return db.GetSendScheduleForAccount(ctx, accountID, scheduleID)
}

// PauseSendScheduleForUser pauses a plan. Frozen instances are untouched.
func (db *DB) PauseSendScheduleForUser(ctx context.Context, userID, scheduleID string) (SendSchedule, error) {
	var accountID string
	err := db.Write().QueryRowContext(ctx, `
		UPDATE send_schedules
		SET status=?, paused_at=COALESCE(paused_at, CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status=? AND account_id IN (SELECT id FROM accounts WHERE user_id=?)
		RETURNING account_id`, SendSchedulePaused, scheduleID, SendScheduleActive, userID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := db.GetSendScheduleForUser(ctx, userID, scheduleID); getErr != nil {
			return SendSchedule{}, ErrSendScheduleNotFound
		}
		return SendSchedule{}, ErrSendScheduleInvalidState
	}
	if err != nil {
		return SendSchedule{}, err
	}
	return db.GetSendScheduleForAccount(ctx, accountID, scheduleID)
}

// ResumeSendScheduleForUser marks paused-due occurrences as skipped(paused)
// in the same transaction that reactivates the plan, so nothing paused over
// is ever delivered on resume.
func (db *DB) ResumeSendScheduleForUser(ctx context.Context, userID, scheduleID string, now time.Time) (SendSchedule, error) {
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return SendSchedule{}, err
	}
	defer tx.Rollback()
	plan, err := scanSendSchedule(tx.QueryRowContext(ctx, sendScheduleSelect+`
		JOIN accounts a ON a.id = s.account_id
		WHERE s.id = ? AND a.user_id = ?`, scheduleID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return SendSchedule{}, ErrSendScheduleNotFound
	}
	if err != nil {
		return SendSchedule{}, err
	}
	if plan.Status != SendSchedulePaused {
		return SendSchedule{}, ErrSendScheduleNotPaused
	}
	loc, err := plan.location()
	if err != nil {
		return SendSchedule{}, err
	}
	state := planMaterializeState{}
	if err := materializePlan(ctx, tx, plan, now.UTC(), loc, false, nil, &ScheduleMaterializeReport{}, &state); err != nil {
		return SendSchedule{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE send_schedules SET status=?, paused_at=NULL,
			cursor_date=?, next_occurrence_at=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		SendScheduleActive, state.cursorDate, nullableInstant(state.nextOccurrence), scheduleID,
	); err != nil {
		return SendSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return SendSchedule{}, err
	}
	return db.GetSendScheduleForAccount(ctx, plan.AccountID, scheduleID)
}

// EndSendScheduleForUser stops future occurrences. Existing instances keep
// their content and result; reason is "user" or "completed".
func (db *DB) EndSendScheduleForUser(ctx context.Context, userID, scheduleID, reason string) (SendSchedule, error) {
	if reason == "" {
		reason = "user"
	}
	var accountID string
	err := db.Write().QueryRowContext(ctx, `
		UPDATE send_schedules
		SET status=?, ended_reason=?, ended_at=COALESCE(ended_at, CURRENT_TIMESTAMP),
			next_occurrence_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status!=? AND account_id IN (SELECT id FROM accounts WHERE user_id=?)
		RETURNING account_id`,
		SendScheduleEnded, reason, scheduleID, SendScheduleEnded, userID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := db.GetSendScheduleForUser(ctx, userID, scheduleID); getErr != nil {
			return SendSchedule{}, ErrSendScheduleNotFound
		}
		return SendSchedule{}, ErrSendScheduleInvalidState
	}
	if err != nil {
		return SendSchedule{}, err
	}
	return db.GetSendScheduleForAccount(ctx, accountID, scheduleID)
}

// PreparedScheduleSend is the frozen payload for one occurrence: a fresh
// independent message identity baked into MessageJSON and its rendered MIME.
type PreparedScheduleSend struct {
	Transport          string
	EnvelopeFrom       string
	EnvelopeRecipients []string
	MIMEData           []byte
	MessageJSON        []byte
}

// ScheduleSendBuilder renders one occurrence's delivery payload. It must be
// pure local work: it runs while the materialization transaction is open.
type ScheduleSendBuilder func(instanceID string, contentJSON []byte, occurrence recurrence.Occurrence) (PreparedScheduleSend, error)

// ScheduleMaterializeReport summarizes one account materialization pass.
type ScheduleMaterializeReport struct {
	ScheduleIDs []string
	Frozen      int
	Skipped     int
	Failed      int
	AutoEnded   []string
}

// planMaterializeState is one plan's cursor result inside a pass.
type planMaterializeState struct {
	cursorDate     string
	nextOccurrence time.Time
}

// MaterializeDueScheduleInstances enumerates active and paused plans on one
// account, records due skips, and freezes the latest due occurrence of each
// active plan into its own outgoing_sends row. Repeated calls are idempotent.
func (db *DB) MaterializeDueScheduleInstances(ctx context.Context, accountID string, now time.Time, build ScheduleSendBuilder) (ScheduleMaterializeReport, error) {
	report := ScheduleMaterializeReport{}
	if build == nil {
		return report, fmt.Errorf("schedule send builder is required")
	}
	now = now.UTC()
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, sendScheduleSelect+`
		WHERE s.account_id=? AND s.status IN ('active','paused')
		ORDER BY s.created_at`, accountID)
	if err != nil {
		return report, err
	}
	var plans []SendSchedule
	for rows.Next() {
		plan, err := scanSendSchedule(rows)
		if err != nil {
			rows.Close()
			return report, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	rows.Close()
	for _, plan := range plans {
		loc, err := plan.location()
		if err != nil {
			// Timezone data cannot change for a validated plan; skip it.
			continue
		}
		state := planMaterializeState{}
		before := report
		if err := materializePlan(ctx, tx, plan, now, loc, plan.Status == SendScheduleActive, build, &report, &state); err != nil {
			return report, err
		}
		changed := (report.Frozen+report.Skipped+report.Failed)-(before.Frozen+before.Skipped+before.Failed)
		if changed > 0 {
			report.ScheduleIDs = append(report.ScheduleIDs, plan.ID)
		}
		rule, _ := plan.rule()
		if _, stillFuture := rule.NextMatch(parseDateOrStart(state.cursorDate, rule.StartDate)); plan.Status == SendScheduleActive && !stillFuture {
			if _, err := tx.ExecContext(ctx, `
				UPDATE send_schedules
				SET status=?, ended_reason='completed', ended_at=CURRENT_TIMESTAMP,
					cursor_date=?, next_occurrence_at=NULL, updated_at=CURRENT_TIMESTAMP
				WHERE id=? AND status='active'`, SendScheduleEnded, state.cursorDate, plan.ID); err != nil {
				return report, err
			}
			report.AutoEnded = append(report.AutoEnded, plan.ID)
		} else {
			if _, err := tx.ExecContext(ctx, `
				UPDATE send_schedules
				SET cursor_date=?, next_occurrence_at=?, updated_at=CURRENT_TIMESTAMP
				WHERE id=?`, state.cursorDate, nullableInstant(state.nextOccurrence), plan.ID); err != nil {
				return report, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func parseDateOrStart(value string, fallback recurrence.CivilDate) recurrence.CivilDate {
	date, err := recurrence.ParseCivilDate(value)
	if err != nil {
		return fallback
	}
	return date
}

// materializePlan enumerates one plan from its cursor through today. Active
// plans freeze the newest due occurrence and mark earlier ones missed;
// paused plans mark every due occurrence skipped. All instance inserts are
// INSERT OR IGNORE against UNIQUE(schedule_id, local_date), so duplicate
// wakeups and a process exit after commit never double-produce a date.
func materializePlan(ctx context.Context, tx *sql.Tx, plan SendSchedule, now time.Time, loc *time.Location, allowFreeze bool, build ScheduleSendBuilder, report *ScheduleMaterializeReport, state *planMaterializeState) error {
	rule, err := plan.rule()
	if err != nil {
		return err
	}
	cursor, err := recurrence.ParseCivilDate(plan.CursorDate)
	if err != nil {
		cursor = rule.StartDate
	}
	today := recurrence.CivilDateAt(now, loc)
	existing, err := existingInstanceDates(ctx, tx, plan.ID, cursor, today)
	if err != nil {
		return err
	}

	type candidate struct {
		occ recurrence.Occurrence
	}
	var due []candidate
	stoppedAt := today.AddDays(1)
	date := cursor
	considered := 0
	for !date.After(today) {
		if considered >= maxScheduleOccurrencesPerTick {
			// Too large a backlog to record safely: advance without freeze.
			stoppedAt = today.AddDays(1)
			due = nil
			break
		}
		if !rule.MatchesDate(date) {
			date = date.AddDays(1)
			continue
		}
		considered++
		occ := rule.Resolve(date, loc)
		if occ.Gap || !occ.Instant.After(now) {
			due = append(due, candidate{occ: occ})
			date = date.AddDays(1)
			continue
		}
		// A matching date later today whose moment has not arrived: keep the
		// cursor parked on it so it freezes exactly when due. Earlier due
		// candidates stay recorded.
		stoppedAt = date
		break
	}

	// At most the newest sendable due date freezes; every older due date
	// records why it did not send.
	freezeIndex := -1
	if allowFreeze {
		for i := len(due) - 1; i >= 0; i-- {
			if due[i].occ.Gap {
				continue
			}
			if _, exists := existing[due[i].occ.Date.String()]; !exists {
				freezeIndex = i
			}
			break
		}
	}
	for i, c := range due {
		if _, exists := existing[c.occ.Date.String()]; exists {
			continue
		}
		if i == freezeIndex {
			if err := freezeScheduleInstance(ctx, tx, plan, c.occ, build, report); err != nil {
				return err
			}
			continue
		}
		reason := SendScheduleSkipMissed
		if !allowFreeze {
			reason = SendScheduleSkipPaused
		}
		if c.occ.Gap {
			reason = SendScheduleSkipDstGap
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO send_schedule_instances
				(id, schedule_id, local_date, scheduled_at, status, skip_reason)
			VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), plan.ID, c.occ.Date.String(), c.occ.Instant.UTC(),
			SendScheduleInstanceSkipped, reason); err != nil {
			return err
		}
		report.Skipped++
	}

	state.cursorDate = stoppedAt.String()
	if next, ok := rule.NextOccurrence(stoppedAt, loc); ok && next.Instant.After(now) {
		state.nextOccurrence = next.Instant.UTC()
	}
	return nil
}

// freezeScheduleInstance builds one occurrence's independent snapshot and
// inserts it together with its outgoing_sends delivery row atomically.
func freezeScheduleInstance(ctx context.Context, tx *sql.Tx, plan SendSchedule, occ recurrence.Occurrence, build ScheduleSendBuilder, report *ScheduleMaterializeReport) error {
	instanceID := uuid.NewString()
	sendID := uuid.NewString()
	prepared, err := build(instanceID, plan.ContentJSON, occ)
	if err != nil {
		if _, insertErr := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO send_schedule_instances
				(id, schedule_id, local_date, scheduled_at, status, last_error)
			VALUES (?, ?, ?, ?, ?, ?)`,
			instanceID, plan.ID, occ.Date.String(), occ.Instant.UTC(),
			SendScheduleInstanceFailed, sanitizeScheduleError(err.Error())); insertErr != nil {
			return insertErr
		}
		report.Failed++
		return nil
	}
	recipientsJSON, err := json.Marshal(prepared.EnvelopeRecipients)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outgoing_sends (
			id, account_id, message_id, draft_id, transport, envelope_from,
			envelope_recipients, mime_data, message_json, send_after, next_attempt_at,
			is_scheduled, schedule_instance_id, status, attempt_count, last_error,
			locked_at, sent_message_id
		) VALUES (?, ?, NULL, '', ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, 1, ?, ?, 0, '', NULL, '')`,
		sendID, plan.AccountID, prepared.Transport, prepared.EnvelopeFrom,
		string(recipientsJSON), prepared.MIMEData, string(prepared.MessageJSON),
		occ.Instant.UTC(), instanceID, SendScheduleInstancePending,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO send_schedule_instances
			(id, schedule_id, local_date, scheduled_at, status, outgoing_send_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		instanceID, plan.ID, occ.Date.String(), occ.Instant.UTC(),
		SendScheduleInstancePending, sendID); err != nil {
		return err
	}
	report.Frozen++
	return nil
}

func existingInstanceDates(ctx context.Context, tx *sql.Tx, scheduleID string, from, to recurrence.CivilDate) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT local_date FROM send_schedule_instances
		WHERE schedule_id=? AND local_date BETWEEN ? AND ?`,
		scheduleID, from.String(), to.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := map[string]bool{}
	for rows.Next() {
		var localDate string
		if err := rows.Scan(&localDate); err != nil {
			return nil, err
		}
		dates[localDate] = true
	}
	return dates, rows.Err()
}

func sanitizeScheduleError(text string) string {
	if len(text) > 500 {
		return text[:500]
	}
	return text
}

// ReconcileScheduleInstances mirrors linked outgoing send statuses onto
// their instances. Skipped instances and rows already gone through retention
// keep their last mirrored state.
func (db *DB) ReconcileScheduleInstances(ctx context.Context, accountID string) error {
	mappedStatus := `CASE os.status
				WHEN 'pending' THEN 'pending'
				WHEN 'sending' THEN 'sending'
				WHEN 'sent' THEN 'sent'
				WHEN 'failed' THEN 'failed'
				WHEN 'ambiguous' THEN 'ambiguous'
				WHEN 'canceled' THEN 'canceled'
				ELSE i.status END`
	_, err := db.Write().ExecContext(ctx, `
		UPDATE send_schedule_instances AS i
		SET status = `+mappedStatus+`,
			attempt_count = os.attempt_count,
			last_error = os.last_error,
			sent_at = CASE WHEN os.status='sent' THEN COALESCE(i.sent_at, CURRENT_TIMESTAMP) ELSE i.sent_at END,
			updated_at = CURRENT_TIMESTAMP
		FROM outgoing_sends AS os
		WHERE i.outgoing_send_id = os.id AND i.schedule_id IN (
			SELECT id FROM send_schedules WHERE account_id=?
		) AND i.status IN ('pending','sending','failed','ambiguous','canceled')
		  AND (i.status <> (`+mappedStatus+`)
		    OR i.attempt_count <> os.attempt_count
		    OR i.last_error <> os.last_error)`, accountID)
	return err
}
