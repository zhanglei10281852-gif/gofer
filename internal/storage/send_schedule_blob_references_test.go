package storage

import "testing"

func TestUserBlobReferencesProtectScheduleContent(t *testing.T) {
	system := newUserStoreTestSystem(t)
	m := newUserStoreTestManager(t, system, UserStoreOptions{MaxOpen: 1})
	db := acquireUserStore(t, m, "alice").DB()
	if _, err := db.Write().Exec(`INSERT INTO accounts(id,user_id,email_address) VALUES('account','alice','alice@example.com')`); err != nil {
		t.Fatal(err)
	}

	content := `{"attachments":[{"path":"/schedules/plan/attachment.txt"}]}`
	insertPlan := func(id, status string) {
		if _, err := db.Write().Exec(`INSERT INTO send_schedules(id,account_id,status,recurrence,timezone,hour,minute,start_date,content_json)
			VALUES(?,'account',?,'daily','UTC',9,0,'2026-01-01',?)`, id, status, content); err != nil {
			t.Fatal(err)
		}
	}
	insertInstance := func(id, planID, status string) {
		if _, err := db.Write().Exec(`INSERT INTO send_schedule_instances(id,schedule_id,local_date,scheduled_at,status)
			VALUES(?,?,'2026-01-02',CURRENT_TIMESTAMP,?)`, id, planID, status); err != nil {
			t.Fatal(err)
		}
	}

	insertPlan("plan-active", "active")
	insertPlan("plan-paused", "paused")
	insertPlan("plan-ended-done", "ended")
	insertPlan("plan-ended-failed", "ended")
	insertInstance("inst-failed", "plan-ended-failed", "failed")
	insertPlan("plan-ended-sent", "ended")
	insertInstance("inst-sent", "plan-ended-sent", "sent")
	insertPlan("plan-ended-pending", "ended")
	insertInstance("inst-pending", "plan-ended-pending", "pending")
	insertPlan("plan-ended-skipped", "ended")
	insertInstance("inst-skipped", "plan-ended-skipped", "skipped")

	keepPath := "/schedules/plan/attachment.txt"
	kept := func() bool {
		var got bool
		if err := db.WithUserBlobReferences(t.Context(), "alice", func(r BlobReferences) error {
			got = r.Paths[keepPath]
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Active and paused plans keep their content even with no instances.
	if !kept() {
		t.Fatal("active/paused schedule content not retained")
	}

	// End every active/paused plan and resolve the pending instance; the
	// ended plan still carrying a failed instance must keep its files.
	if _, err := db.Write().Exec(`UPDATE send_schedule_instances SET status='sent' WHERE schedule_id='plan-ended-pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`UPDATE send_schedules SET status='ended' WHERE id IN ('plan-active','plan-paused')`); err != nil {
		t.Fatal(err)
	}
	if !kept() {
		t.Fatal("ended plan with a failed instance released its files early")
	}

	// Resolve the last unfinished instance to a terminal state: everything
	// is ended with only sent/skipped history, so the files release.
	if _, err := db.Write().Exec(`UPDATE send_schedule_instances SET status='sent' WHERE schedule_id='plan-ended-failed'`); err != nil {
		t.Fatal(err)
	}
	if kept() {
		t.Fatal("ended plan content retained after every instance finished")
	}
}

func TestUserBlobReferencesRejectMalformedScheduleContent(t *testing.T) {
	system := newUserStoreTestSystem(t)
	m := newUserStoreTestManager(t, system, UserStoreOptions{MaxOpen: 1})
	db := acquireUserStore(t, m, "alice").DB()
	if _, err := db.Write().Exec(`INSERT INTO accounts(id,user_id,email_address) VALUES('account','alice','alice@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`INSERT INTO send_schedules(id,account_id,recurrence,timezone,hour,minute,start_date,content_json)
		VALUES('plan-bad','account','daily','UTC',9,0,'2026-01-01','not-json')`); err != nil {
		t.Fatal(err)
	}
	if err := db.WithUserBlobReferences(t.Context(), "alice", func(BlobReferences) error { return nil }); err == nil {
		t.Fatal("malformed schedule content accepted by blob references")
	}
}
