package executor

import (
	"fmt"
	"strings"
	"testing"
)

func TestShapeOutputTailAndMetadata(t *testing.T) {
	got, err := ShapeOutput("line1\nline2\nline3\n", "", "tail", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.TotalBytes != 18 || got.TotalLines != 3 || len(got.SHA256) != 12 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if !strings.HasPrefix(got.Stdout, "[truncated: kept 7 of 18 bytes / 3 lines; sha256=") {
		t.Fatalf("missing truncation marker: %q", got.Stdout)
	}
	if !strings.HasSuffix(got.Stdout, "line3\n") {
		t.Fatalf("tail not preserved: %q", got.Stdout)
	}
}

func TestShapeOutputHeadCountNoneAndRaw(t *testing.T) {
	input := "abcdefghij"
	head, err := ShapeOutput(input, "", "head", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !head.Truncated || !strings.HasSuffix(head.Stdout, "abcd") {
		t.Fatalf("head not preserved: %+v", head)
	}
	count, err := ShapeOutput(input, "", "count", 4)
	if err != nil {
		t.Fatal(err)
	}
	if count.Stdout != "" || count.Stderr != "" || count.TotalBytes != 10 || count.TotalLines != 1 {
		t.Fatalf("bad count: %+v", count)
	}
	none, err := ShapeOutput(input, "", "none", 4)
	if err != nil {
		t.Fatal(err)
	}
	if none.Stdout != "" || none.Stderr != "" || !none.Truncated {
		t.Fatalf("bad none: %+v", none)
	}
	raw, err := ShapeOutput("small", "err", "raw", 20)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Truncated || raw.Stdout != "small" || raw.Stderr != "err" {
		t.Fatalf("raw changed: %+v", raw)
	}
}

func TestShapeOutputMaxBytesAndModeValidation(t *testing.T) {
	if _, err := NormalizeOutputOptions("other", 20); err == nil {
		t.Fatal("expected invalid mode")
	}
	if _, err := NormalizeOutputOptions("raw", MaxOutputBytes+1); err == nil {
		t.Fatal("expected max_bytes rejection")
	}
	input := strings.Repeat("x", 5000)
	got, err := ShapeOutput(input, "", "raw", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.TotalBytes != 5000 || !strings.Contains(got.Stdout, fmt.Sprintf("kept 4096 of 5000 bytes")) {
		t.Fatalf("default truncation missing: %+v", got)
	}
}
