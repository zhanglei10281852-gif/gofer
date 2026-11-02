package storage

import "database/sql"

// migrateV133ToV134 adds recurring send schedules: their plans, per-date
// instances, and the outgoing_sends link tying a frozen delivery snapshot to
// its instance.
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
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS send_schedules (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'ended')),
			recurrence TEXT NOT NULL CHECK (recurrence IN ('daily', 'weekly')),
			timezone TEXT NOT NULL,
			hour INTEGER NOT NULL CHECK (hour BETWEEN 0 AND 23),
			minute INTEGER NOT NULL CHECK (minute BETWEEN 0 AND 59),
			start_date TEXT NOT NULL,
			end_date TEXT NOT NULL DEFAULT '',
			weekdays TEXT NOT NULL DEFAULT '',
			content_json TEXT NOT NULL DEFAULT '',
			cursor_date TEXT NOT NULL DEFAULT '',
			next_occurrence_at DATETIME,
			ended_reason TEXT NOT NULL DEFAULT '',
			paused_at DATETIME,
			ended_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_send_schedules_account
			ON send_schedules(account_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_send_schedules_next
			ON send_schedules(status, next_occurrence_at) WHERE status = 'active'`,
		`CREATE TABLE IF NOT EXISTS send_schedule_instances (
			id TEXT PRIMARY KEY,
			schedule_id TEXT NOT NULL REFERENCES send_schedules(id) ON DELETE CASCADE,
			local_date TEXT NOT NULL,
			scheduled_at DATETIME NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'ambiguous', 'canceled', 'skipped')),
			skip_reason TEXT NOT NULL DEFAULT '' CHECK (skip_reason IN ('', 'dst_gap', 'missed', 'paused')),
			outgoing_send_id TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			sent_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(schedule_id, local_date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_send_schedule_instances_schedule
			ON send_schedule_instances(schedule_id, local_date)`,
		`CREATE INDEX IF NOT EXISTS idx_send_schedule_instances_status
			ON send_schedule_instances(status, scheduled_at)`,
		`CREATE INDEX IF NOT EXISTS idx_send_schedule_instances_send
			ON send_schedule_instances(outgoing_send_id) WHERE outgoing_send_id <> ''`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	exists, err := tableExistsTx(tx, "outgoing_sends")
	if err != nil {
		return err
	}
	if exists {
		present, err := columnExistsTx(tx, "outgoing_sends", "schedule_instance_id")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE outgoing_sends ADD COLUMN schedule_instance_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_outgoing_sends_schedule_instance
			ON outgoing_sends(schedule_instance_id) WHERE schedule_instance_id <> ''`); err != nil {
			return err
		}
	}
	if err := markSchemaVersion(tx, 134); err != nil {
		return err
	}
	return tx.Commit()
}
