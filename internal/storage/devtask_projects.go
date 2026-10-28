package storage

import (
	"database/sql"
	"errors"
	"time"
)

// DevProject is one registered devtask project. The *JSON fields hold raw JSON arrays.
type DevProject struct {
	Name                  string
	RepoPath              string
	BaseRef               string
	DefaultModel          string
	AllowedCmdsJSON       string
	DefaultAcceptCmdsJSON string
	ForbiddenPathsJSON    string
	MaxRounds             int
	MaxSteps              int
	TimeoutS              int
	Enabled               bool
	CreatedAt             int64
	UpdatedAt             int64
}

// ErrDevProjectNotFound is returned by GetDevProject for an unknown project name.
var ErrDevProjectNotFound = errors.New("devtask project not found")

const devProjectColumns = "name,repo_path,base_ref,default_model,allowed_cmds_json,default_accept_cmds_json,forbidden_paths_json,max_rounds,max_steps,timeout_s,enabled,created_at,updated_at"

func scanDevProject(sc interface{ Scan(...any) error }) (DevProject, error) {
	var p DevProject
	var enabled int
	err := sc.Scan(&p.Name, &p.RepoPath, &p.BaseRef, &p.DefaultModel, &p.AllowedCmdsJSON, &p.DefaultAcceptCmdsJSON,
		&p.ForbiddenPathsJSON, &p.MaxRounds, &p.MaxSteps, &p.TimeoutS, &enabled, &p.CreatedAt, &p.UpdatedAt)
	p.Enabled = enabled != 0
	return p, err
}

// GetDevProject returns one project by name.
func (s *Store) GetDevProject(name string) (DevProject, error) {
	p, err := scanDevProject(s.DB.QueryRow("SELECT "+devProjectColumns+" FROM dev_projects WHERE name=?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return DevProject{}, ErrDevProjectNotFound
	}
	return p, err
}

// ListDevProjects returns every project ordered by name.
func (s *Store) ListDevProjects() ([]DevProject, error) {
	rows, err := s.DB.Query("SELECT " + devProjectColumns + " FROM dev_projects ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevProject
	for rows.Next() {
		p, err := scanDevProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertDevProject inserts or replaces a project. created_at is kept for an existing name.
// Validation of values (absolute paths, model whitelist) belongs to the API layer.
func (s *Store) UpsertDevProject(p DevProject) error {
	now := time.Now().Unix()
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err := s.DB.Exec(`INSERT INTO dev_projects(`+devProjectColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET repo_path=excluded.repo_path, base_ref=excluded.base_ref,
		default_model=excluded.default_model, allowed_cmds_json=excluded.allowed_cmds_json,
		default_accept_cmds_json=excluded.default_accept_cmds_json, forbidden_paths_json=excluded.forbidden_paths_json,
		max_rounds=excluded.max_rounds, max_steps=excluded.max_steps, timeout_s=excluded.timeout_s,
		enabled=excluded.enabled, updated_at=excluded.updated_at`,
		p.Name, p.RepoPath, p.BaseRef, p.DefaultModel, p.AllowedCmdsJSON, p.DefaultAcceptCmdsJSON, p.ForbiddenPathsJSON,
		p.MaxRounds, p.MaxSteps, p.TimeoutS, enabled, now, now)
	return err
}
