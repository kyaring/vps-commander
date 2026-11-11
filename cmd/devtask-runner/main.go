package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/wjyhk/vps-commander/internal/devtask"
)

func main() {
	once := flag.Bool("once", false, "claim and process at most one task then exit")
	flag.Parse()

	base := stringsTrim(os.Getenv("HUB_URL"))
	token := stringsTrim(os.Getenv("RUNNER_TOKEN"))
	if base == "" || token == "" {
		log.Fatal("HUB_URL and RUNNER_TOKEN are required")
	}
	work := stringsTrim(os.Getenv("WORK_ROOT"))
	if work == "" {
		work = "/var/lib/devtask-runner"
	}
	lease := envInt("LEASE_SEC", 120)
	poll := envInt("POLL_SEC", 5)

	hub := &devtask.HubClient{BaseURL: base, Token: token, ClaimedBy: stringsTrim(os.Getenv("RUNNER_ID"))}
	var model devtask.ModelClient
	if api := stringsTrim(os.Getenv("MODEL_API_BASE")); api != "" {
		model = &openAIClient{BaseURL: api, APIKey: stringsTrim(os.Getenv("MODEL_API_KEY")), HTTP: &http.Client{Timeout: 120 * time.Second}}
	}
	r := &devtask.Runner{Hub: hub, Model: model, Config: devtask.RunnerConfig{WorkRoot: work, LeaseSec: lease, PollSec: poll}}
	ctx := context.Background()
	if *once {
		handled, err := r.RunOnce(ctx)
		if err != nil {
			log.Fatalf("run once: %v", err)
		}
		if !handled {
			log.Print("no task")
		}
		return
	}
	for {
		handled, err := r.RunOnce(ctx)
		if err != nil {
			log.Printf("error: %v", err)
		}
		if !handled {
			time.Sleep(time.Duration(poll) * time.Second)
		}
	}
}

func envInt(k string, def int) int {
	v := stringsTrim(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

type openAIClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func (c *openAIClient) Complete(ctx context.Context, model, system, user string) (string, int, error) {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stringsTrim(c.BaseURL)+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("model http %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", resp.StatusCode, err
	}
	if len(out.Choices) == 0 {
		return "", resp.StatusCode, fmt.Errorf("empty choices")
	}
	return out.Choices[0].Message.Content, resp.StatusCode, nil
}
