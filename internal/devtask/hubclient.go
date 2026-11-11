package devtask

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HubClient talks to the DevTask runner REST API.
type HubClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	ClaimedBy  string
}

func (c *HubClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *HubClient) do(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client().Do(req)
}

// Task is the JSON shape returned by claim/get.
type Task struct {
	ID             string   `json:"id"`
	Project        string   `json:"project"`
	Title          string   `json:"title"`
	SpecMD         string   `json:"spec_md"`
	AcceptCmds     []string `json:"accept_cmds"`
	AllowedPaths   []string `json:"allowed_paths"`
	ForbiddenPaths []string `json:"forbidden_paths"`
	BaseRef        string   `json:"base_ref"`
	BaseSHA        string   `json:"base_sha"`
	Branch         string   `json:"branch"`
	Model          string   `json:"model"`
	Status         string   `json:"status"`
	Round          int      `json:"round"`
	MaxRounds      int      `json:"max_rounds"`
	MaxSteps       int      `json:"max_steps"`
	TimeoutS       int      `json:"timeout_s"`
	AllowedCmds    []string `json:"allowed_cmds"`
}

// Claim returns a task or nil when the hub responds 204.
func (c *HubClient) Claim(leaseSec int) (*Task, error) {
	body := map[string]any{"lease_sec": leaseSec}
	if c.ClaimedBy != "" {
		body["claimed_by"] = c.ClaimedBy
	}
	resp, err := c.do(http.MethodPost, "/api/v1/devtasks/claim", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claim: %s: %s", resp.Status, truncate(string(b), 200))
	}
	var t Task
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *HubClient) Heartbeat(id string, leaseSec int) error {
	resp, err := c.do(http.MethodPost, "/api/v1/devtasks/"+id+"/heartbeat", map[string]any{"lease_sec": leaseSec})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat: %s", resp.Status)
	}
	return nil
}

func (c *HubClient) PostEvents(id string, events []map[string]any) error {
	resp, err := c.do(http.MethodPost, "/api/v1/devtasks/"+id+"/events", map[string]any{"events": events})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}
	return nil
}

// CompleteResult is sent to the hub.
type CompleteResult struct {
	Status      string `json:"status"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	DiffStat    string `json:"diff_stat,omitempty"`
	TestSummary string `json:"test_summary,omitempty"`
	ResultMD    string `json:"result_md,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
}

func (c *HubClient) Complete(id string, r CompleteResult) error {
	resp, err := c.do(http.MethodPost, "/api/v1/devtasks/"+id+"/complete", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete: %s: %s", resp.Status, truncate(string(b), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
