package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListDirectoryAndInfo(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	es, err := ListDirectory(d, false, 10)
	if err != nil || len(es) != 2 {
		t.Fatalf("entries=%v err=%v", es, err)
	}
	fi, err := GetFileInfo(filepath.Join(d, "a.txt"))
	if err != nil || fi.Size != 5 {
		t.Fatalf("info=%+v err=%v", fi, err)
	}
}
func TestSearchHonorsContextAndLimit(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < 3; i++ {
		_ = os.WriteFile(filepath.Join(d, string(rune('a'+i))+".txt"), []byte("needle\n"), 0600)
	}
	r, err := Search(context.Background(), d, "needle", 2)
	if err != nil || len(r) != 2 {
		t.Fatalf("results=%v err=%v", r, err)
	}
}
