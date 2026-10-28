package storage

import (
	"errors"
	"testing"
)

func TestDevProjectUpsertGetList(t *testing.T) {
	s, _ := openDevTaskStore(t)
	p := DevProject{Name: "wjy-web", RepoPath: "/srv/repos/wjy-web", BaseRef: "main", DefaultModel: DevTaskDefaultModel,
		AllowedCmdsJSON: `["go test"]`, DefaultAcceptCmdsJSON: `["go vet ./..."]`, ForbiddenPathsJSON: `[".git"]`,
		MaxRounds: 3, MaxSteps: 50, TimeoutS: 1800, Enabled: true}
	if err := s.UpsertDevProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevProject("wjy-web")
	if err != nil || got.RepoPath != p.RepoPath || got.MaxRounds != 3 || !got.Enabled || got.AllowedCmdsJSON != `["go test"]` {
		t.Fatalf("get = %+v, %v", got, err)
	}
	created := got.CreatedAt
	p.MaxRounds, p.Enabled = 5, false
	if err := s.UpsertDevProject(p); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDevProject("wjy-web")
	if got.MaxRounds != 5 || got.Enabled || got.CreatedAt != created {
		t.Fatalf("after upsert = %+v (created_at must be kept)", got)
	}
	list, err := s.ListDevProjects()
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if _, err := s.GetDevProject("missing"); !errors.Is(err, ErrDevProjectNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
}
