package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	mailpkg "github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/recurrence"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func newScheduleHandlerTest(t *testing.T) (*Handler, *storage.DB) {
	t.Helper()
	h, db := newAccountOwnershipTestHandler(t)
	h.syncer = mailpkg.NewSyncOrchestrator(db, nil, nil, nil)
	return h, db
}

func scheduleRequest(t *testing.T, userID, path string, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: userID, Username: userID}))
}

func seedDraftsFolder(t *testing.T, db *storage.DB, accountID string) {
	t.Helper()
	if _, err := db.Write().Exec(`INSERT INTO folders (id, account_id, remote_id, name, role)
		VALUES ('victim-drafts', ?, 'Drafts', 'Drafts', 'drafts')`, accountID); err != nil {
		t.Fatalf("drafts folder: %v", err)
	}
}

func dailyForm(now time.Time) url.Values {
	date := now.Add(26 * time.Hour).Format("2006-01-02")
	return url.Values{
		"account_id":        {"victim-account"},
		"to":                {"recipient@example.com"},
		"subject":           {"Weekly report"},
		"body":              {"Remember to send it."},
		"schedule_date":     {date},
		"schedule_hour":     {"09"},
		"schedule_minute":   {"00"},
		"schedule_timezone": {"UTC"},
		"schedule_repeat":   {"daily"},
	}
}

func TestHandleComposeScheduleCreatesRecurringPlanAndDropsDraft(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	seedDraftsFolder(t, db, "victim-account")

	rec := httptest.NewRecorder()
	h.handleComposeSchedule(rec, scheduleRequest(t, "owner", "/compose/schedule", dailyForm(time.Now())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["schedule_id"] == "" || response["schedule_repeat"] != "daily" {
		t.Fatalf("response = %v", response)
	}
	planID := response["schedule_id"]

	plans, err := db.ListSendSchedulesForUser(t.Context(), "owner")
	if err != nil || len(plans) != 1 || plans[0].ID != planID {
		t.Fatalf("plans = %+v %v", plans, err)
	}
	if !strings.Contains(string(plans[0].ContentJSON), "Weekly report") {
		t.Fatalf("plan content = %s", plans[0].ContentJSON)
	}
	if !plans[0].HasNextOccurrence {
		t.Fatal("plan has no next occurrence")
	}
	// The creation draft is gone, and no message-linked single send exists.
	var draftMessages int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM messages m
		JOIN message_folder_state s ON s.message_id=m.id
		WHERE s.is_draft=1 AND m.account_id='victim-account'`).Scan(&draftMessages); err != nil {
		t.Fatal(err)
	}
	if draftMessages != 0 {
		t.Fatalf("source draft survived: %d", draftMessages)
	}
	var linkedSends int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM outgoing_sends WHERE schedule_instance_id='' AND is_scheduled=1`).Scan(&linkedSends); err != nil {
		t.Fatal(err)
	}
	if linkedSends != 0 {
		t.Fatalf("recurring creation scheduled a single send: %d", linkedSends)
	}
	// The plan copied its content into plan-owned storage.
	var accountID, contentJSON string
	if err := db.Read().QueryRow(`SELECT account_id, content_json FROM send_schedules WHERE id=?`, planID).Scan(&accountID, &contentJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot outgoingMessageSnapshot
	if err := json.Unmarshal([]byte(contentJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Subject != "Weekly report" || snapshot.MessageID != "" || snapshot.InReplyTo != "" {
		t.Fatalf("content template = %+v", snapshot)
	}
	planDir := filepath.Join(h.blobStore.BasePath(), accountID, "schedules", planID)
	if entries, err := os.ReadDir(planDir); err != nil || len(entries) != 1 {
		t.Fatalf("plan content dir = %v, %v", entries, err)
	}
}

func TestHandleComposeScheduleRejectsInvalidRecurringRules(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	seedDraftsFolder(t, db, "victim-account")

	cases := []url.Values{
		func() url.Values {
			f := dailyForm(time.Now())
			f.Set("schedule_repeat", "weekly")
			f.Del("schedule_weekdays")
			return f
		}(),
		func() url.Values {
			f := dailyForm(time.Now())
			f.Set("schedule_minute", "07")
			return f
		}(),
		func() url.Values {
			f := dailyForm(time.Now())
			f.Set("schedule_timezone", "Not/AZone")
			return f
		}(),
		func() url.Values {
			f := dailyForm(time.Now())
			f.Set("schedule_end_date", time.Now().AddDate(0, 0, -1).Format("2006-01-02"))
			return f
		}(),
	}
	for i, form := range cases {
		rec := httptest.NewRecorder()
		h.handleComposeSchedule(rec, scheduleRequest(t, "owner", "/compose/schedule", form))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("case %d: status = %d body = %q", i, rec.Code, rec.Body.String())
		}
	}
	var plans int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM send_schedules`).Scan(&plans); err != nil || plans != 0 {
		t.Fatalf("invalid rules created plans: %d %v", plans, err)
	}
}

func TestHandleComposeScheduleNoneRepeatStillQueuesSingle(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	seedDraftsFolder(t, db, "victim-account")
	form := dailyForm(time.Now())
	form.Set("schedule_repeat", "none")

	rec := httptest.NewRecorder()
	h.handleComposeSchedule(rec, scheduleRequest(t, "owner", "/compose/schedule", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["schedule_id"] != "" {
		t.Fatalf("single schedule created a plan: %v", response)
	}
	var count, instanceLinked int
	if err := db.Read().QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN schedule_instance_id<>'' THEN 1 ELSE 0 END),0) FROM outgoing_sends WHERE is_scheduled=1`).Scan(&count, &instanceLinked); err != nil {
		t.Fatal(err)
	}
	if count != 1 || instanceLinked != 0 {
		t.Fatalf("single sends = %d, plan-linked = %d", count, instanceLinked)
	}
}

func TestSendScheduleAPIOwnership(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	seedDraftsFolder(t, db, "victim-account")
	rec := httptest.NewRecorder()
	h.handleComposeSchedule(rec, scheduleRequest(t, "owner", "/compose/schedule", dailyForm(time.Now())))
	var response map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&response)
	planID := response["schedule_id"]

	// Attacker gets an empty plan list and 404 on every plan endpoint.
	listRec := httptest.NewRecorder()
	h.handleSendScheduleList(listRec, scheduleRequest(t, "attacker", "/api/send-schedules", url.Values{}))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"schedules":[]`) {
		t.Fatalf("attacker list = %d %s", listRec.Code, listRec.Body.String())
	}
	for _, handler := range []func(http.ResponseWriter, *http.Request){
		h.handleSendScheduleGet, h.handleSendScheduleInstances,
	} {
		r := httptest.NewRecorder()
		req := scheduleRequest(t, "attacker", "/api/send-schedules/"+planID, url.Values{})
		req.SetPathValue("id", planID)
		handler(r, req)
		if r.Code != http.StatusNotFound {
			t.Fatalf("attacker status = %d", r.Code)
		}
	}
}

func TestScheduleContentEditKeepsFrozenSnapshot(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	cfg, err := h.accountStore.GetConfig(t.Context(), "victim-account")
	if err != nil {
		t.Fatal(err)
	}
	// Plan whose first occurrence (yesterday 23:59) is already due.
	today := time.Now().UTC()
	plan, err := db.CreateSendSchedule(t.Context(), storage.CreateSendScheduleInput{
		AccountID: "victim-account", Recurrence: "daily", Timezone: "UTC",
		Hour: 23, Minute: 59,
		StartDate:   today.AddDate(0, 0, -1).Format("2006-01-02"),
		ContentJSON: []byte(`{"from_email":"owner@example.com","to":[{"address":"recipient@example.com"}],"subject":"old","text_body":"old body","attachments":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := db.MaterializeDueScheduleInstances(t.Context(), "victim-account", today, h.scheduleSendBuilder(cfg))
	if err != nil || report.Frozen != 1 {
		t.Fatalf("freeze = %+v %v", report, err)
	}
	instances, err := db.GetSendScheduleInstancesForUser(t.Context(), "owner", plan.ID, 10)
	if err != nil || len(instances) != 1 {
		t.Fatalf("instances = %v %v", instances, err)
	}
	frozenSendID := instances[0].OutgoingSendID
	before, err := db.GetOutgoingSend(t.Context(), frozenSendID)
	if err != nil {
		t.Fatal(err)
	}

	// Edit future content: the frozen outgoing snapshot must not change.
	form := url.Values{
		"account_id": {"victim-account"},
		"to":         {"recipient@example.com"},
		"subject":    {"new subject"},
		"body":       {"new body"},
	}
	r := httptest.NewRecorder()
	req := scheduleRequest(t, "owner", "/api/send-schedules/"+plan.ID+"/content", form)
	req.SetPathValue("id", plan.ID)
	h.handleSendScheduleContent(r, req)
	if r.Code != http.StatusOK {
		t.Fatalf("content update = %d %s", r.Code, r.Body.String())
	}
	after, err := db.GetOutgoingSend(t.Context(), frozenSendID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.MessageJSON) != string(before.MessageJSON) || string(after.MIMEData) != string(before.MIMEData) {
		t.Fatal("frozen snapshot changed after content edit")
	}
	updated, err := db.GetSendScheduleForAccount(t.Context(), "victim-account", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated.ContentJSON), "new subject") {
		t.Fatalf("plan content not replaced: %s", updated.ContentJSON)
	}
}

func TestSchedulePauseResumeEndLifecycle(t *testing.T) {
	h, db := newScheduleHandlerTest(t)
	seedDraftsFolder(t, db, "victim-account")
	rec := httptest.NewRecorder()
	h.handleComposeSchedule(rec, scheduleRequest(t, "owner", "/compose/schedule", dailyForm(time.Now())))
	var response map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&response)
	planID := response["schedule_id"]

	call := func(action string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		req := scheduleRequest(t, "owner", "/api/send-schedules/"+planID+"/"+action, url.Values{})
		req.SetPathValue("id", planID)
		req.SetPathValue("action", action)
		h.handleSendScheduleAction(r, req)
		return r
	}
	if r := call("pause"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"paused"`) {
		t.Fatalf("pause = %d %s", r.Code, r.Body.String())
	}
	if r := call("pause"); r.Code != http.StatusConflict {
		t.Fatalf("second pause = %d", r.Code)
	}
	if r := call("resume"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"active"`) {
		t.Fatalf("resume = %d %s", r.Code, r.Body.String())
	}
	if r := call("end"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"status":"ended"`) {
		t.Fatalf("end = %d %s", r.Code, r.Body.String())
	}
	if r := call("pause"); r.Code != http.StatusConflict {
		t.Fatalf("pause after end = %d", r.Code)
	}
}

func TestScheduleBuilderGivesEachOccurrenceIndependentIdentity(t *testing.T) {
	h, _ := newAccountOwnershipTestHandler(t)
	cfg, err := h.accountStore.GetConfig(t.Context(), "victim-account")
	if err != nil {
		t.Fatal(err)
	}
	builder := h.scheduleSendBuilder(cfg)
	content := []byte(`{"from_email":"owner@example.com","to":[{"address":"recipient@example.com"}],"subject":"hi","text_body":"body"}`)
	loc, _ := time.LoadLocation("UTC")
	rule := recurrence.Rule{Recurrence: recurrence.Daily, Timezone: "UTC", Hour: 9, Minute: 0}
	date1, _ := recurrence.ParseCivilDate("2026-03-01")
	date2, _ := recurrence.ParseCivilDate("2026-03-02")
	occ1 := rule.Resolve(date1, loc)
	occ2 := rule.Resolve(date2, loc)
	first, err := builder("instance-1", content, occ1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder("instance-2", content, occ2)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.MIMEData) == string(second.MIMEData) {
		t.Fatal("consecutive occurrences rendered identical MIME")
	}
	var s1, s2 outgoingMessageSnapshot
	if err := json.Unmarshal(first.MessageJSON, &s1); err != nil || json.Unmarshal(second.MessageJSON, &s2) != nil {
		t.Fatal(err)
	}
	if s1.MessageID == "" || s1.MessageID == s2.MessageID {
		t.Fatalf("message ids = %q %q", s1.MessageID, s2.MessageID)
	}
	if !s1.Date.Equal(occ1.Instant) || !s2.Date.Equal(occ2.Instant) {
		t.Fatalf("dates = %s %s", s1.Date, s2.Date)
	}
	if !strings.Contains(string(first.MIMEData), "Subject: hi") {
		t.Fatalf("mime missing subject: %q", first.MIMEData)
	}
	// A content template without recipients cannot be frozen.
	bad, err := builder("x", []byte(`{"subject":"no recipients"}`), occ1)
	if err == nil || bad.MIMEData != nil {
		t.Fatalf("builder accepted recipient-free content: %+v", bad)
	}
}
