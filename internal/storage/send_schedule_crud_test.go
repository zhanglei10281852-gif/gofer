package storage

import (
	"errors"
	"testing"
	"time"
)

func TestSendScheduleCRUDOwnershipAndTransitions(t *testing.T) {
	db := newScheduleTestDB(t)
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('bob','bob','bob')`); err != nil {
		t.Fatal(err)
	}

	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	start := tomorrow.Format("2006-01-02")
	plan, err := db.CreateSendSchedule(t.Context(), CreateSendScheduleInput{
		AccountID:   "account",
		Recurrence:  "daily",
		Timezone:    "UTC",
		Hour:        23,
		Minute:      30,
		StartDate:   start,
		ContentJSON: []byte(`{"subject":"weekly report"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != SendScheduleActive || !plan.HasNextOccurrence || plan.CursorDate != start {
		t.Fatalf("created plan = %+v", plan)
	}

	if _, err := db.GetSendScheduleForUser(t.Context(), "alice", plan.ID); err != nil {
		t.Fatalf("owner get: %v", err)
	}
	if _, err := db.GetSendScheduleForUser(t.Context(), "bob", plan.ID); !errors.Is(err, ErrSendScheduleNotFound) {
		t.Fatalf("cross-user get = %v", err)
	}
	list, err := db.ListSendSchedulesForUser(t.Context(), "alice")
	if err != nil || len(list) != 1 || list[0].ID != plan.ID {
		t.Fatalf("list = %v %v", list, err)
	}
	if n, err := db.CountActiveSchedulesForUser(t.Context(), "alice"); err != nil || n != 1 {
		t.Fatalf("count = %d %v", n, err)
	}

	// Settings update applies and validates.
	updated, err := db.UpdateSendScheduleSettingsForUser(t.Context(), "alice", plan.ID, UpdateSendScheduleSettingsInput{
		Recurrence: "weekly", Timezone: "UTC", Hour: 8, Minute: 0, Weekdays: "1,3",
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if updated.Recurrence != "weekly" || updated.Weekdays != "1,3" {
		t.Fatalf("updated plan = %+v", updated)
	}
	if _, err := db.UpdateSendScheduleSettingsForUser(t.Context(), "alice", plan.ID, UpdateSendScheduleSettingsInput{
		Recurrence: "weekly", Timezone: "UTC", Hour: 8, Minute: 0, Weekdays: "",
	}); err == nil {
		t.Fatal("weekly plan without weekdays accepted")
	}
	if _, err := db.UpdateSendScheduleSettingsForUser(t.Context(), "alice", plan.ID, UpdateSendScheduleSettingsInput{
		Recurrence: "weekly", Timezone: "UTC", Hour: 8, Minute: 0, Weekdays: "1",
	}); err != nil {
		t.Fatalf("cross-user check setup: %v", err)
	}

	// Content replacement never touches frozen instances (none yet here).
	updated2, err := db.UpdateSendScheduleContentForUser(t.Context(), "alice", plan.ID, []byte(`{"subject":"new"}`))
	if err != nil {
		t.Fatalf("update content: %v", err)
	}
	if string(updated2.ContentJSON) != `{"subject":"new"}` {
		t.Fatalf("content = %s", updated2.ContentJSON)
	}
	if _, err := db.UpdateSendScheduleContentForUser(t.Context(), "bob", plan.ID, []byte(`{}`)); !errors.Is(err, ErrSendScheduleNotFound) {
		t.Fatalf("cross-user content update = %v", err)
	}

	// Pause -> pause again invalid -> resume -> end -> settings locked.
	if _, err := db.PauseSendScheduleForUser(t.Context(), "alice", plan.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := db.PauseSendScheduleForUser(t.Context(), "alice", plan.ID); !errors.Is(err, ErrSendScheduleInvalidState) {
		t.Fatalf("second pause = %v", err)
	}
	resumed, err := db.ResumeSendScheduleForUser(t.Context(), "alice", plan.ID, time.Now().UTC())
	if err != nil || resumed.Status != SendScheduleActive {
		t.Fatalf("resume = %+v %v", resumed, err)
	}
	if _, err := db.ResumeSendScheduleForUser(t.Context(), "alice", plan.ID, time.Now().UTC()); !errors.Is(err, ErrSendScheduleNotPaused) {
		t.Fatalf("active resume = %v", err)
	}
	ended, err := db.EndSendScheduleForUser(t.Context(), "alice", plan.ID, "user")
	if err != nil || ended.Status != SendScheduleEnded || ended.EndedReason != "user" || ended.HasNextOccurrence {
		t.Fatalf("end = %+v %v", ended, err)
	}
	if _, err := db.PauseSendScheduleForUser(t.Context(), "alice", plan.ID); !errors.Is(err, ErrSendScheduleInvalidState) {
		t.Fatalf("pause ended = %v", err)
	}
	if _, err := db.UpdateSendScheduleSettingsForUser(t.Context(), "alice", plan.ID, UpdateSendScheduleSettingsInput{
		Recurrence: "daily", Timezone: "UTC", Hour: 8, Minute: 0,
	}); !errors.Is(err, ErrSendScheduleNotActive) {
		t.Fatalf("settings update on ended plan = %v", err)
	}
	if n, err := db.CountActiveSchedulesForUser(t.Context(), "alice"); err != nil || n != 0 {
		t.Fatalf("count after end = %d %v", n, err)
	}

	// Instances list stays owner-scoped and empty for a fresh plan.
	instances, err := db.GetSendScheduleInstancesForUser(t.Context(), "alice", plan.ID, 50)
	if err != nil || len(instances) != 0 {
		t.Fatalf("instances = %v %v", instances, err)
	}
	if _, err := db.GetSendScheduleInstancesForUser(t.Context(), "bob", plan.ID, 50); err != nil {
		t.Fatalf("cross-user instances should return an empty list, got %v", err)
	}
}

func TestCreateSendScheduleValidatesRule(t *testing.T) {
	db := newScheduleTestDB(t)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	base := CreateSendScheduleInput{
		AccountID: "account", Recurrence: "daily", Timezone: "UTC",
		Hour: 9, Minute: 0, StartDate: tomorrow, ContentJSON: []byte(`{}`),
	}
	invalid := []CreateSendScheduleInput{
		base,
	}
	invalid[0].Timezone = "Not/AZone"
	invalid = append(invalid, base, base)
	invalid[1].Recurrence = "weekly"
	invalid[1].Weekdays = ""
	invalid[2].Hour = 24

	for i, input := range invalid {
		if _, err := db.CreateSendSchedule(t.Context(), input); err == nil {
			t.Fatalf("invalid input %d accepted: %+v", i, input)
		}
	}
	if _, err := db.CreateSendSchedule(t.Context(), base); err != nil {
		t.Fatalf("valid create: %v", err)
	}
}
