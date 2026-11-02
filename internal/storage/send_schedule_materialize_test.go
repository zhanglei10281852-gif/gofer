package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/recurrence"
)

func utcTime(date string, hour int) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", fmt.Sprintf("%s %02d:00:00", date, hour))
	if err != nil {
		panic(err)
	}
	return t
}

func newScheduleTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('alice','alice','alice');
		INSERT INTO accounts(id,user_id,email_address) VALUES('account','alice','alice@example.com')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertTestPlan(t *testing.T, db *DB, p map[string]string) {
	t.Helper()
	get := func(key, def string) string {
		if v, ok := p[key]; ok && v != "" {
			return v
		}
		return def
	}
	hour, minute := 9, 0
	if v := get("hour", ""); v != "" {
		if _, err := fmt.Sscanf(v, "%d:%d", &hour, &minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Write().Exec(`
		INSERT INTO send_schedules (
			id, account_id, status, recurrence, timezone, hour, minute,
			start_date, end_date, weekdays, content_json, cursor_date, next_occurrence_at
		) VALUES (?, 'account', ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, NULL)`,
		get("id", "plan"), get("status", "active"), get("recurrence", "daily"),
		get("timezone", "UTC"), hour, minute,
		get("start", "2026-03-01"), get("end", ""), get("weekdays", ""),
		get("cursor", get("start", "2026-03-01")),
	); err != nil {
		t.Fatal(err)
	}
}

func fakeScheduleBuilder(failDates map[string]bool) ScheduleSendBuilder {
	return func(instanceID string, content []byte, occ recurrence.Occurrence) (PreparedScheduleSend, error) {
		if failDates[occ.Date.String()] {
			return PreparedScheduleSend{}, errors.New("builder refused this occurrence")
		}
		return PreparedScheduleSend{
			Transport:          "smtp",
			EnvelopeFrom:       "alice@example.com",
			EnvelopeRecipients: []string{"bob@example.com"},
			MIMEData:           []byte("MIME-" + occ.Date.String()),
			MessageJSON:        []byte(fmt.Sprintf(`{"message_id":"%s"}`, instanceID)),
		}, nil
	}
}

func listTestInstances(t *testing.T, db *DB) map[string]SendScheduleInstance {
	t.Helper()
	rows, err := db.Read().Query(`
		SELECT id, schedule_id, local_date, scheduled_at, status, skip_reason,
			outgoing_send_id, last_error, attempt_count, sent_at, created_at, updated_at
		FROM send_schedule_instances ORDER BY local_date`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]SendScheduleInstance{}
	for rows.Next() {
		instance, err := scanSendScheduleInstance(rows)
		if err != nil {
			t.Fatal(err)
		}
		out[instance.LocalDate] = instance
	}
	return out
}

func TestMaterializeDailyDownTimeSendsOnlyLatestAndAdvances(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01"})

	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-04", 10), fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || report.Skipped != 3 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	if len(instances) != 4 {
		t.Fatalf("instances = %v", instances)
	}
	for _, date := range []string{"2026-03-01", "2026-03-02", "2026-03-03"} {
		if instances[date].Status != SendScheduleInstanceSkipped || instances[date].SkipReason != SendScheduleSkipMissed {
			t.Fatalf("%s = %+v", date, instances[date])
		}
	}
	frozen := instances["2026-03-04"]
	if frozen.Status != SendScheduleInstancePending || frozen.OutgoingSendID == "" {
		t.Fatalf("latest instance not frozen: %+v", frozen)
	}
	plan, err := db.GetSendScheduleForAccount(t.Context(), "account", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if plan.CursorDate != "2026-03-05" {
		t.Fatalf("cursor = %s", plan.CursorDate)
	}
	if !plan.HasNextOccurrence || !plan.NextOccurrenceAt.Equal(utcTime("2026-03-05", 9)) {
		t.Fatalf("next = %v (%v)", plan.NextOccurrenceAt, plan.HasNextOccurrence)
	}
	var mime string
	if err := db.Read().QueryRow(`SELECT mime_data FROM outgoing_sends WHERE id=?`, frozen.OutgoingSendID).Scan(&mime); err != nil {
		t.Fatal(err)
	}
	if mime != "MIME-2026-03-04" {
		t.Fatalf("mime = %q", mime)
	}

	// A duplicate wake changes nothing.
	again, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-04", 10), fakeScheduleBuilder(nil))
	if err != nil || again.Frozen != 0 || again.Skipped != 0 {
		t.Fatalf("duplicate wake: %+v %v", again, err)
	}
	if len(listTestInstances(t, db)) != 4 {
		t.Fatal("duplicate instances produced")
	}

	// The next day freezes independently even if the first send is parked on
	// a far-future retry deadline: retry scheduling never anchors the cycle.
	if _, err := db.Write().Exec(`UPDATE outgoing_sends SET next_attempt_at=? WHERE id=?`,
		utcTime("2026-03-20", 0), frozen.OutgoingSendID); err != nil {
		t.Fatal(err)
	}
	next, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-05", 10), fakeScheduleBuilder(nil))
	if err != nil || next.Frozen != 1 {
		t.Fatalf("next day: %+v %v", next, err)
	}
	instances = listTestInstances(t, db)
	if instances["2026-03-05"].Status != SendScheduleInstancePending {
		t.Fatalf("03-05 instance = %+v", instances["2026-03-05"])
	}
	var ids []string
	rows, _ := db.Read().Query(`SELECT id FROM outgoing_sends WHERE schedule_instance_id<>'' ORDER BY send_after`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("send ids = %v", ids)
	}
}

func TestMaterializeKeepsPastDueWhenLaterTodayIsFuture(t *testing.T) {
	db := newScheduleTestDB(t)
	// 23:59 daily: at 10:00 the previous date's occurrence is due while
	// today's occurrence is still in the future.
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "hour": "23:59", "start": "2026-03-01", "cursor": "2026-03-01",
	})
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || report.Skipped != 2 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	if instances["2026-03-03"].Status != SendScheduleInstancePending {
		t.Fatalf("03-03 not frozen: %+v", instances["2026-03-03"])
	}
	plan, err := db.GetSendScheduleForAccount(t.Context(), "account", "plan")
	if err != nil || plan.CursorDate != "2026-03-04" {
		t.Fatalf("cursor parked on today = %q", plan.CursorDate)
	}
}

func TestMaterializeWeeklyFiltersWeekdays(t *testing.T) {
	db := newScheduleTestDB(t)
	// Monday 2026-03-02 and Wednesday 2026-03-04 only.
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "recurrence": "weekly", "weekdays": "1,3",
		"start": "2026-03-02", "cursor": "2026-03-02",
	})
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-09", 10), fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || report.Skipped != 2 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	for _, date := range []string{"2026-03-02", "2026-03-04"} {
		if instances[date].Status != SendScheduleInstanceSkipped {
			t.Fatalf("%s = %+v", date, instances[date])
		}
	}
	if instances["2026-03-09"].Status != SendScheduleInstancePending {
		t.Fatalf("monday 03-09 not frozen: %+v", instances["2026-03-09"])
	}
	for _, off := range []string{"2026-03-03", "2026-03-05", "2026-03-06", "2026-03-07", "2026-03-08"} {
		if _, ok := instances[off]; ok {
			t.Fatalf("non-weekday %s got an instance", off)
		}
	}
}

func TestMaterializeDSTGapIsSkipped(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "timezone": "America/New_York", "hour": "2:30",
		"start": "2026-03-07", "cursor": "2026-03-07", "end": "2026-03-09",
	})
	// 2026-03-09 12:00 UTC = 08:00 EDT, after the 02:30 moments.
	now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || report.Skipped != 2 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	gap := instances["2026-03-08"]
	if gap.Status != SendScheduleInstanceSkipped || gap.SkipReason != SendScheduleSkipDstGap || gap.OutgoingSendID != "" {
		t.Fatalf("gap instance = %+v", gap)
	}
	if instances["2026-03-09"].Status != SendScheduleInstancePending {
		t.Fatalf("post-gap day not frozen: %+v", instances["2026-03-09"])
	}
	if n := countOutgoingForPlan(t, db); n != 1 {
		t.Fatalf("outgoing sends = %d, want 1", n)
	}
}

func TestMaterializeDSTAmbiguousSendsOnceAtEarlierInstant(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "timezone": "America/New_York", "hour": "1:30",
		"start": "2026-10-31", "cursor": "2026-10-31", "end": "2026-11-01",
	})
	// 2026-11-01 12:00 UTC = 08:00 EST, after both copies of 01:30.
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || report.Skipped != 1 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	amb := instances["2026-11-01"]
	if amb.Status != SendScheduleInstancePending || amb.OutgoingSendID == "" {
		t.Fatalf("ambiguous instance = %+v", amb)
	}
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // earlier EDT copy
	if !amb.ScheduledAt.Equal(want) {
		t.Fatalf("scheduled at %s, want earlier %s", amb.ScheduledAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// Repeated wakeups never produce a second send for that local date.
	for range 3 {
		if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil)); err != nil {
			t.Fatal(err)
		}
	}
	if n := countOutgoingForPlan(t, db); n != 1 {
		t.Fatalf("outgoing sends after repeated wakes = %d, want 1", n)
	}
}

func TestMaterializePausedSkipsAndResumeDoesNotBackfill(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "status": "paused", "start": "2026-03-01", "cursor": "2026-03-01",
	})
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-03", 10), fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 0 || report.Skipped != 3 {
		t.Fatalf("paused report = %+v", report)
	}
	for _, date := range []string{"2026-03-01", "2026-03-02", "2026-03-03"} {
		if got := listTestInstances(t, db)[date]; got.SkipReason != SendScheduleSkipPaused {
			t.Fatalf("%s = %+v", date, got)
		}
	}
	if n := countOutgoingForPlan(t, db); n != 0 {
		t.Fatalf("paused plan produced sends: %d", n)
	}

	// Resume on 03-05 marks the 04th/05th paused and continues from the 06th.
	plan, err := db.ResumeSendScheduleForUser(t.Context(), "alice", "plan", utcTime("2026-03-05", 10))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != SendScheduleActive {
		t.Fatalf("status = %s", plan.Status)
	}
	instances := listTestInstances(t, db)
	for _, date := range []string{"2026-03-04", "2026-03-05"} {
		if instances[date].SkipReason != SendScheduleSkipPaused {
			t.Fatalf("resume backfilled %s: %+v", date, instances[date])
		}
	}
	if plan.CursorDate != "2026-03-06" || !plan.NextOccurrenceAt.Equal(utcTime("2026-03-06", 9)) {
		t.Fatalf("after resume cursor=%s next=%s", plan.CursorDate, plan.NextOccurrenceAt)
	}
	// The next due occurrence after resume sends normally.
	if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-06", 10), fakeScheduleBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	if got := listTestInstances(t, db)["2026-03-06"]; got.Status != SendScheduleInstancePending {
		t.Fatalf("post-resume day = %+v", got)
	}
}

func TestPauseKeepsAlreadyFrozenInstance(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01"})
	if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-02", 10), fakeScheduleBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	before := listTestInstances(t, db)["2026-03-02"]
	if before.Status != SendScheduleInstancePending {
		t.Fatalf("setup: %+v", before)
	}
	if _, err := db.PauseSendScheduleForUser(t.Context(), "alice", "plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-03", 10), fakeScheduleBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	after := listTestInstances(t, db)
	if after["2026-03-02"].Status != before.Status || after["2026-03-02"].OutgoingSendID != before.OutgoingSendID {
		t.Fatalf("frozen instance changed across pause: %+v vs %+v", before, after["2026-03-02"])
	}
	if after["2026-03-03"].SkipReason != SendScheduleSkipPaused {
		t.Fatalf("03-03 = %+v", after["2026-03-03"])
	}
}

func TestMaterializeBuilderFailureFailsOneDateAndContinues(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01"})
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-02", 10),
		fakeScheduleBuilder(map[string]bool{"2026-03-02": true}))
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 || report.Frozen != 0 {
		t.Fatalf("report = %+v", report)
	}
	instances := listTestInstances(t, db)
	failed := instances["2026-03-02"]
	if failed.Status != SendScheduleInstanceFailed || failed.OutgoingSendID != "" || failed.LastError == "" {
		t.Fatalf("failed instance = %+v", failed)
	}
	if n := countOutgoingForPlan(t, db); n != 0 {
		t.Fatalf("builder failure left an outgoing row: %d", n)
	}
	// The cursor advanced; the next day freezes independently.
	report, err = db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-03", 10), fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 {
		t.Fatalf("follow-up report = %+v", report)
	}
	if got := listTestInstances(t, db)["2026-03-03"]; got.Status != SendScheduleInstancePending {
		t.Fatalf("03-03 = %+v", got)
	}
}

func TestMaterializeAutoEndsAtEndDate(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{
		"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01", "end": "2026-03-03",
	})
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-04", 10), fakeScheduleBuilder(nil))
	if err != nil {
		t.Fatal(err)
	}
	if report.Frozen != 1 || len(report.AutoEnded) != 1 {
		t.Fatalf("report = %+v", report)
	}
	plan, err := db.GetSendScheduleForAccount(t.Context(), "account", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != SendScheduleEnded || plan.EndedReason != "completed" || plan.HasNextOccurrence {
		t.Fatalf("plan = %+v", plan)
	}
	if got := listTestInstances(t, db)["2026-03-03"]; got.Status != SendScheduleInstancePending {
		t.Fatalf("end-date occurrence = %+v", got)
	}
	// An ended plan materializes nothing afterwards.
	again, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-10", 10), fakeScheduleBuilder(nil))
	if err != nil || again.Frozen != 0 || again.Skipped != 0 {
		t.Fatalf("ended plan materialized: %+v %v", again, err)
	}
}

func TestReconcileMirrorsOutgoingStatusAndKeepsHistory(t *testing.T) {
	db := newScheduleTestDB(t)
	insertTestPlan(t, db, map[string]string{"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01"})
	if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", utcTime("2026-03-01", 10), fakeScheduleBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	instance := listTestInstances(t, db)["2026-03-01"]
	sendID := instance.OutgoingSendID

	for _, step := range []struct {
		status     string
		wantStatus string
		attempt    int
	}{
		{"sending", SendScheduleInstanceSending, 1},
		{"ambiguous", SendScheduleInstanceAmbiguous, 1},
		{"pending", SendScheduleInstancePending, 2},
		{"sent", SendScheduleInstanceSent, 2},
	} {
		if _, err := db.Write().Exec(`UPDATE outgoing_sends SET status=?, attempt_count=? WHERE id=?`, step.status, step.attempt, sendID); err != nil {
			t.Fatal(err)
		}
		if err := db.ReconcileScheduleInstances(t.Context(), "account"); err != nil {
			t.Fatal(err)
		}
		got := listTestInstances(t, db)["2026-03-01"]
		if got.Status != step.wantStatus || got.AttemptCount != step.attempt {
			t.Fatalf("after %s: %+v want %s", step.status, got, step.wantStatus)
		}
	}
	sent := listTestInstances(t, db)["2026-03-01"]
	if sent.SentAt.IsZero() {
		t.Fatal("sent_at not mirrored")
	}
	// Retention deletes the delivery row; the instance keeps its result.
	if _, err := db.Write().Exec(`DELETE FROM outgoing_sends WHERE id=?`, sendID); err != nil {
		t.Fatal(err)
	}
	if err := db.ReconcileScheduleInstances(t.Context(), "account"); err != nil {
		t.Fatal(err)
	}
	if got := listTestInstances(t, db)["2026-03-01"]; got.Status != SendScheduleInstanceSent {
		t.Fatalf("history lost after send-row deletion: %+v", got)
	}
}

func TestMaterializeRestartIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir())
	db, err := New(filepath.Join(path, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('alice','alice','alice');
		INSERT INTO accounts(id,user_id,email_address) VALUES('account','alice','alice@example.com')`); err != nil {
		t.Fatal(err)
	}
	insertTestPlan(t, db, map[string]string{"id": "plan", "start": "2026-03-01", "cursor": "2026-03-01"})
	now := utcTime("2026-03-02", 10)
	if _, err := db.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a process restart: reopen, reconcile, materialize again.
	db2, err := New(filepath.Join(path, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	if err := db2.ReconcileScheduleInstances(t.Context(), "account"); err != nil {
		t.Fatal(err)
	}
	again, err := db2.MaterializeDueScheduleInstances(t.Context(), "account", now, fakeScheduleBuilder(nil))
	if err != nil || again.Frozen != 0 {
		t.Fatalf("post-restart materialization: %+v %v", again, err)
	}
	if n := countOutgoingForPlan(t, db2); n != 1 {
		t.Fatalf("outgoing sends after restart = %d, want 1", n)
	}
	plan, err := db2.GetSendScheduleForAccount(t.Context(), "account", "plan")
	if err != nil || plan.CursorDate != "2026-03-03" {
		t.Fatalf("plan after restart: %+v %v", plan, err)
	}
}

func TestScheduledFolderCountCombinesPlansAndSingleSends(t *testing.T) {
	db := newScheduleTestDB(t)
	if _, err := db.Write().Exec(`
		INSERT INTO folders (id, account_id, remote_id, name, role)
		VALUES ('sched-drafts', 'account', 'Drafts', 'Drafts', 'drafts');
		INSERT INTO messages (id, account_id, internet_message_id, date_received, snippet)
		VALUES (7, 'account', '<sched-7@example.com>', CURRENT_TIMESTAMP, 'waiting');
		INSERT INTO message_folder_state (message_id, folder_id, is_draft, is_deleted)
		VALUES (7, 'sched-drafts', 1, 0);
		INSERT INTO outgoing_sends (
			id, account_id, message_id, transport, envelope_from, envelope_recipients,
			mime_data, message_json, send_after, is_scheduled, schedule_instance_id, status
		) VALUES ('single-send','account',7,'smtp','a@example.com','["b@example.com"]',
			x'00', '{}', CURRENT_TIMESTAMP, 1, '', 'pending');`); err != nil {
		t.Fatal(err)
	}
	if n, err := db.GetScheduledFolderCountForUser(t.Context(), "alice"); err != nil || n != 1 {
		t.Fatalf("count with single send = %d %v", n, err)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if _, err := db.CreateSendSchedule(t.Context(), CreateSendScheduleInput{
		AccountID: "account", Recurrence: "daily", Timezone: "UTC",
		Hour: 23, Minute: 0, StartDate: tomorrow, ContentJSON: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.GetScheduledFolderCountForUser(t.Context(), "alice"); err != nil || n != 2 {
		t.Fatalf("count with plan = %d %v", n, err)
	}
	// The intercepted folder-count route reports the same number.
	if n, err := db.GetFolderEmailCountFilteredForUser(t.Context(), "alice", "scheduled", models.EmailFilters{}); err != nil || n != 2 {
		t.Fatalf("folder count route = %d %v", n, err)
	}
}

func countOutgoingForPlan(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM outgoing_sends WHERE schedule_instance_id<>''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
