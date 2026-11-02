package storage

import (
	"path/filepath"
	"testing"
)

func TestFreshSchemaIncludesSendSchedules(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version int
	if err := db.Read().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion)
	}
	for _, table := range []string{"send_schedules", "send_schedule_instances"} {
		var count int
		if err := db.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s missing", table)
		}
	}
	present, err := columnExists(db.Write(), "outgoing_sends", "schedule_instance_id")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("outgoing_sends.schedule_instance_id missing on fresh schema")
	}
}

func TestMigrateV133ToV134UpgradesLegacyDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gofer.db")
	raw, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_version (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO schema_version (version) VALUES (133);
		CREATE TABLE users (id TEXT PRIMARY KEY);
		CREATE TABLE accounts (id TEXT PRIMARY KEY, user_id TEXT);
		CREATE TABLE outgoing_sends (
			id TEXT PRIMARY KEY,
			account_id TEXT,
			transport TEXT NOT NULL DEFAULT 'smtp',
			envelope_from TEXT NOT NULL DEFAULT '',
			send_after DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO outgoing_sends (id, account_id) VALUES ('legacy-send', 'legacy-account');
	`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("reopen legacy database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version int
	if err := db.Read().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 134 {
		t.Fatalf("migrated version = %d, want 134", version)
	}
	for _, table := range []string{"send_schedules", "send_schedule_instances"} {
		var count int
		if err := db.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s missing after migration", table)
		}
	}
	present, err := columnExists(db.Write(), "outgoing_sends", "schedule_instance_id")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("schedule_instance_id not added to legacy outgoing_sends")
	}
	var instanceID string
	if err := db.Read().QueryRow(`SELECT schedule_instance_id FROM outgoing_sends WHERE id='legacy-send'`).Scan(&instanceID); err != nil {
		t.Fatalf("legacy outgoing row lost: %v", err)
	}
	if instanceID != "" {
		t.Fatalf("legacy schedule_instance_id = %q, want empty", instanceID)
	}

	// The migration must be idempotent on already-current databases.
	if err := migrateV133ToV134(db.Write()); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
}
