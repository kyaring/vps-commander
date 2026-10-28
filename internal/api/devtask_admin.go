package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wjyhk/vps-commander/internal/storage"
)

// devtaskEnabled reads the DEVTASK_ENABLED switch. It is off unless set to on, true or 1.
func devtaskEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEVTASK_ENABLED"))) {
	case "on", "true", "1":
		return true
	}
	return false
}

// devtaskGate answers 404 while the feature is off, so disabled routes look absent.
func devtaskGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !devtaskEnabled() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var devProjectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// devProjectJSON is the wire form of a project. Command and path lists are JSON arrays.
type devProjectJSON struct {
	Name           string   `json:"name"`
	RepoPath       string   `json:"repo_path"`
	BaseRef        string   `json:"base_ref"`
	DefaultModel   string   `json:"default_model"`
	AllowedCmds    []string `json:"allowed_cmds"`
	DefaultAccept  []string `json:"default_accept_cmds"`
	ForbiddenPaths []string `json:"forbidden_paths"`
	MaxRounds      int      `json:"max_rounds"`
	MaxSteps       int      `json:"max_steps"`
	TimeoutS       int      `json:"timeout_s"`
	Enabled        bool     `json:"enabled"`
	ModelOK        *bool    `json:"model_ok,omitempty"`
}

func marshalStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalStrings(raw string) []string {
	var v []string
	_ = json.Unmarshal([]byte(raw), &v)
	if v == nil {
		v = []string{}
	}
	return v
}

func projectToJSON(p storage.DevProject) devProjectJSON {
	return devProjectJSON{Name: p.Name, RepoPath: p.RepoPath, BaseRef: p.BaseRef, DefaultModel: p.DefaultModel,
		AllowedCmds: unmarshalStrings(p.AllowedCmdsJSON), DefaultAccept: unmarshalStrings(p.DefaultAcceptCmdsJSON),
		ForbiddenPaths: unmarshalStrings(p.ForbiddenPathsJSON), MaxRounds: p.MaxRounds, MaxSteps: p.MaxSteps,
		TimeoutS: p.TimeoutS, Enabled: p.Enabled}
}

// validateDevProject checks one submitted project against the rules in the DevTask spec.
func validateDevProject(in devProjectJSON, allowed []string) error {
	if !devProjectNameRE.MatchString(in.Name) {
		return fmt.Errorf("name must be 1-64 chars of a-z, 0-9, _ or -")
	}
	if !filepath.IsAbs(in.RepoPath) {
		return fmt.Errorf("repo_path must be an absolute path")
	}
	if in.BaseRef == "" {
		in.BaseRef = "main"
	}
	if in.MaxRounds < 1 || in.MaxSteps < 1 || in.TimeoutS < 1 {
		return fmt.Errorf("max_rounds, max_steps and timeout_s must be at least 1")
	}
	if !containsString(allowed, in.DefaultModel) {
		return fmt.Errorf("default_model %q is not in the devtask model whitelist", in.DefaultModel)
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// devtaskProjects serves GET (list, with model_ok) and POST (upsert) for admins.
func (s *Server) devtaskProjects(w http.ResponseWriter, r *http.Request) {
	allowed, err := s.Store.GetDevTaskAllowedModels()
	if err != nil {
		http.Error(w, "read model whitelist failed", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		projects, err := s.Store.ListDevProjects()
		if err != nil {
			http.Error(w, "list projects failed", http.StatusInternalServerError)
			return
		}
		out := make([]devProjectJSON, 0, len(projects))
		for _, p := range projects {
			j := projectToJSON(p)
			ok := containsString(allowed, p.DefaultModel)
			j.ModelOK = &ok
			out = append(out, j)
		}
		jsonOut(w, map[string]any{"projects": out})
	case http.MethodPost:
		var in devProjectJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := validateDevProject(in, allowed); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.BaseRef == "" {
			in.BaseRef = "main"
		}
		oldText := "none"
		if old, err := s.Store.GetDevProject(in.Name); err == nil {
			b, _ := json.Marshal(projectToJSON(old))
			oldText = string(b)
		}
		if err := s.Store.UpsertDevProject(storage.DevProject{Name: in.Name, RepoPath: in.RepoPath, BaseRef: in.BaseRef,
			DefaultModel: in.DefaultModel, AllowedCmdsJSON: marshalStrings(in.AllowedCmds),
			DefaultAcceptCmdsJSON: marshalStrings(in.DefaultAccept), ForbiddenPathsJSON: marshalStrings(in.ForbiddenPaths),
			MaxRounds: in.MaxRounds, MaxSteps: in.MaxSteps, TimeoutS: in.TimeoutS, Enabled: in.Enabled}); err != nil {
			http.Error(w, "save project failed", http.StatusInternalServerError)
			return
		}
		newB, _ := json.Marshal(in)
		_ = s.audit(r, in.Name, "devtask_project_upsert", "", 0, 0, fmt.Sprintf("old=%s new=%s", oldText, string(newB)))
		jsonOut(w, map[string]any{"ok": true, "name": in.Name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// devtaskSettings serves GET and PUT of the model whitelist and the feature switch.
func (s *Server) devtaskSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		allowed, err := s.Store.GetDevTaskAllowedModels()
		if err != nil {
			http.Error(w, "read model whitelist failed", http.StatusInternalServerError)
			return
		}
		jsonOut(w, map[string]any{"allowed_models": allowed, "enabled": devtaskEnabled()})
	case http.MethodPut:
		var in struct {
			AllowedModels []string `json:"allowed_models"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		old, err := s.Store.GetDevTaskAllowedModels()
		if err != nil {
			http.Error(w, "read model whitelist failed", http.StatusInternalServerError)
			return
		}
		if err := s.Store.SetDevTaskAllowedModels(in.AllowedModels); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "must not be empty") || strings.Contains(msg, "duplicate model") {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
			http.Error(w, "write model whitelist failed", http.StatusInternalServerError)
			return
		}
		_ = s.audit(r, "", "devtask_settings_update", "", 0, 0, fmt.Sprintf("old=%s new=%s", marshalStrings(old), marshalStrings(in.AllowedModels)))
		jsonOut(w, map[string]any{"ok": true, "allowed_models": in.AllowedModels})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
