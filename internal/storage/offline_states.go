package storage

import "time"

// OfflineState stores notification progress for a node's current offline episode.
type OfflineState struct {
	Device    string
	Since     int64
	Sent      int
	Notified  bool
	UpdatedAt int64
}

func (s *Store) migrateOfflineStates() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS notification_offline_states (
		device TEXT PRIMARY KEY,
		since INTEGER NOT NULL,
		sent INTEGER NOT NULL DEFAULT 0,
		notified INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	)`)
	return err
}

func (s *Store) ListOfflineStates() ([]OfflineState, error) {
	rows, err := s.DB.Query(`SELECT device,since,sent,notified,updated_at FROM notification_offline_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfflineState{}
	for rows.Next() {
		var v OfflineState
		var notified int
		if err := rows.Scan(&v.Device, &v.Since, &v.Sent, &notified, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Notified = notified != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveOfflineState(v OfflineState) error {
	v.UpdatedAt = time.Now().Unix()
	notified := 0
	if v.Notified {
		notified = 1
	}
	_, err := s.DB.Exec(`INSERT INTO notification_offline_states(device,since,sent,notified,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(device) DO UPDATE SET since=excluded.since,sent=excluded.sent,notified=excluded.notified,updated_at=excluded.updated_at`,
		v.Device, v.Since, v.Sent, notified, v.UpdatedAt)
	return err
}

func (s *Store) DeleteOfflineState(device string) error {
	_, err := s.DB.Exec(`DELETE FROM notification_offline_states WHERE device=?`, device)
	return err
}
