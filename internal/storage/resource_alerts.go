package storage

import "time"

// ResourceAlertState stores the small amount of resource-monitor state needed
// to resume an alert episode after a Hub restart.
type ResourceAlertState struct {
	Device        string
	Metric        string
	Label         string
	StartedAt     int64
	Sent          int
	Value         float64
	Threshold     float64
	StrikeCount   int
	RecoveryCount int
	Active        bool
	UpdatedAt     int64
}

func (s *Store) migrateResourceAlertStates() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS resource_alert_states (
		device TEXT NOT NULL,
		metric TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL DEFAULT 0,
		sent INTEGER NOT NULL DEFAULT 0,
		value REAL NOT NULL DEFAULT 0,
		threshold REAL NOT NULL DEFAULT 0,
		strike_count INTEGER NOT NULL DEFAULT 0,
		recovery_count INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY(device, metric)
	)`)
	return err
}

func (s *Store) ListResourceAlertStates() ([]ResourceAlertState, error) {
	rows, err := s.DB.Query(`SELECT device,metric,label,started_at,sent,value,threshold,strike_count,recovery_count,active,updated_at FROM resource_alert_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResourceAlertState{}
	for rows.Next() {
		var v ResourceAlertState
		var active int
		if err := rows.Scan(&v.Device, &v.Metric, &v.Label, &v.StartedAt, &v.Sent, &v.Value, &v.Threshold, &v.StrikeCount, &v.RecoveryCount, &active, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Active = active != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveResourceAlertState(v ResourceAlertState) error {
	v.UpdatedAt = time.Now().Unix()
	active := 0
	if v.Active {
		active = 1
	}
	_, err := s.DB.Exec(`INSERT INTO resource_alert_states(device,metric,label,started_at,sent,value,threshold,strike_count,recovery_count,active,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(device,metric) DO UPDATE SET label=excluded.label,started_at=excluded.started_at,sent=excluded.sent,value=excluded.value,threshold=excluded.threshold,strike_count=excluded.strike_count,recovery_count=excluded.recovery_count,active=excluded.active,updated_at=excluded.updated_at`,
		v.Device, v.Metric, v.Label, v.StartedAt, v.Sent, v.Value, v.Threshold, v.StrikeCount, v.RecoveryCount, active, v.UpdatedAt)
	return err
}

func (s *Store) DeleteResourceAlertState(device, metric string) error {
	_, err := s.DB.Exec(`DELETE FROM resource_alert_states WHERE device=? AND metric=?`, device, metric)
	return err
}
