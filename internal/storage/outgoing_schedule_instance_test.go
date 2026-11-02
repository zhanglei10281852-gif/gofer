package storage

import (
	"context"
	"testing"
	"time"
)

func TestQueueOutgoingSendAllowsMultipleScheduleInstances(t *testing.T) {
	db := newScheduleTestDB(t)
	base := func(id, instanceID string) QueueOutgoingSendInput {
		return QueueOutgoingSendInput{
			ID: id, AccountID: "account", Transport: OutgoingTransportSMTP,
			EnvelopeFrom: "alice@example.com", EnvelopeRecipients: []string{"bob@example.com"},
			MIMEData:           []byte("mime-" + id),
			MessageJSON:        []byte(`{"message_id":"<` + id + `>"}`),
			SendAfter:          time.Now().Add(-time.Minute),
			IsScheduled:        true,
			ScheduleInstanceID: instanceID,
		}
	}
	first, err := db.QueueOutgoingSend(t.Context(), base("send-1", "instance-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.QueueOutgoingSend(t.Context(), base("send-2", "instance-2"))
	if err != nil {
		t.Fatalf("second schedule instance: %v", err)
	}
	if first.ID == second.ID || first.ScheduleInstanceID == second.ScheduleInstanceID {
		t.Fatalf("instances collided: %+v %+v", first, second)
	}

	// Both are claimable, one at a time; completing one never touches the
	// other's snapshot.
	claimed1, err := db.ClaimDueOutgoingSends(t.Context(), time.Now(), 1)
	if err != nil || len(claimed1) != 1 || claimed1[0].ScheduleInstanceID != "instance-1" {
		t.Fatalf("claim 1 = %+v %v", claimed1, err)
	}
	if err := db.CompleteOutgoingSend(t.Context(), first.ID, "<send-1>", false); err != nil {
		t.Fatalf("complete 1: %v", err)
	}
	claimed2, err := db.ClaimDueOutgoingSends(t.Context(), time.Now(), 1)
	if err != nil || len(claimed2) != 1 || claimed2[0].ID != "send-2" {
		t.Fatalf("claim 2 = %+v %v", claimed2, err)
	}
	if string(claimed2[0].MIMEData) != "mime-send-2" {
		t.Fatalf("second snapshot changed: %q", claimed2[0].MIMEData)
	}

	// Reconciliation mirrors the first instance to sent and keeps the second pending.
	ctx := context.Background()
	if _, err := db.Write().Exec(`
		INSERT INTO send_schedules (id,account_id,recurrence,timezone,hour,minute,start_date,content_json)
		VALUES ('plan','account','daily','UTC',9,0,'2026-01-01','{}');
		INSERT INTO send_schedule_instances (id,schedule_id,local_date,scheduled_at,status,outgoing_send_id)
		VALUES ('instance-1','plan','2026-03-01',CURRENT_TIMESTAMP,'pending','send-1'),
		       ('instance-2','plan','2026-03-02',CURRENT_TIMESTAMP,'pending','send-2')`); err != nil {
		t.Fatal(err)
	}
	if err := db.ReconcileScheduleInstances(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	instances := map[string]SendScheduleInstance{}
	rows, err := db.Read().Query(`SELECT id,schedule_id,local_date,scheduled_at,status,skip_reason,outgoing_send_id,last_error,attempt_count,sent_at,created_at,updated_at FROM send_schedule_instances`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		instance, err := scanSendScheduleInstance(rows)
		if err != nil {
			t.Fatal(err)
		}
		instances[instance.ID] = instance
	}
	rows.Close()
	if instances["instance-1"].Status != SendScheduleInstanceSent {
		t.Fatalf("instance-1 = %+v", instances["instance-1"])
	}
	if instances["instance-2"].Status != SendScheduleInstanceSending {
		t.Fatalf("instance-2 = %+v", instances["instance-2"])
	}
}
