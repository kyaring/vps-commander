package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wjyhk/vps-commander/internal/auth"
	"github.com/wjyhk/vps-commander/internal/executor"
	"github.com/wjyhk/vps-commander/internal/mcp"
	"github.com/wjyhk/vps-commander/internal/storage"
)

func newSpillTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hub", "commander.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err := store.SetGlobalSecurityMode("high"); err != nil {
		t.Fatal(err)
	}
	return &Server{Store: store, LocalName: "local"}, dbPath
}

func spillFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// 溢出文件完整保存 stdout+stderr，返回的 sha256 与文件内容一致（前 12 位，与既有格式相同）。
func TestSpillStoresFullOutputAndHashMatchesFile(t *testing.T) {
	s, dbPath := newSpillTestServer(t)
	stdout := string(bytes.Repeat([]byte("0123456789abcdef"), 1<<17)) // 2 MiB
	stderr := "err-part\n"
	resp, err := s.execResponse("tok-A", "local", 0, stdout, stderr, 5, executor.OutputOptions{Mode: "raw", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	spill, _ := resp["spill_path"].(string)
	if spill == "" {
		t.Fatalf("no spill_path for truncated output: %#v", resp)
	}
	if resp["spill_device"] != mcp.SpillDeviceName {
		t.Fatalf("spill_device = %v", resp["spill_device"])
	}
	if filepath.Dir(spill) != filepath.Join(filepath.Dir(dbPath), spillDirName) && filepath.Dir(spill) != mustResolve(t, filepath.Join(filepath.Dir(dbPath), spillDirName)) {
		t.Fatalf("spill written outside the Hub data directory: %s", spill)
	}
	data, err := os.ReadFile(spill)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != stdout+stderr {
		t.Fatalf("spill content differs from full output (len %d vs %d)", len(data), len(stdout+stderr))
	}
	sum := sha256.Sum256(data)
	if got, _ := resp["sha256"].(string); got != hex.EncodeToString(sum[:])[:12] {
		t.Fatalf("sha256 = %v, file = %x", resp["sha256"], sum[:6])
	}
}

func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// 分页读取（read_file 同样的偏移与长度）能拼回完整输出。
func TestSpillPagedReadReconstructsOutput(t *testing.T) {
	s, _ := newSpillTestServer(t)
	full := strings.Repeat("abcdefghij", 200000) // 2,000,000 bytes
	resp, err := s.execResponse("tok-A", "local", 0, full, "", 1, executor.OutputOptions{Mode: "head", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	spill := resp["spill_path"].(string)
	var got strings.Builder
	var off int64
	for {
		c, err := s.readSpillPage("tok-A", spill, off, 512<<10)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(c.Content)
		off += c.Bytes
		if c.EOF || !c.HasMore {
			break
		}
	}
	if got.String() != full {
		t.Fatalf("paged read differs: got %d bytes, want %d", got.Len(), len(full))
	}
	mid, err := s.readSpillPage("tok-A", spill, 1234, 10)
	if err != nil || mid.Content != full[1234:1244] {
		t.Fatalf("middle read = %q, %v", mid.Content, err)
	}
}

// 没有被截断时不写文件，spill_path 为空串。
func TestSpillNotWrittenWhenOutputFits(t *testing.T) {
	s, dbPath := newSpillTestServer(t)
	resp, err := s.execResponse("tok-A", "local", 0, "small", "", 1, executor.OutputOptions{Mode: "raw", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if resp["spill_path"] != "" || resp["truncated"] != false {
		t.Fatalf("unexpected spill for small output: %#v", resp)
	}
	if files := spillFilesIn(t, filepath.Join(filepath.Dir(dbPath), spillDirName)); len(files) != 0 {
		t.Fatalf("files written for small output: %v", files)
	}
}

// 五种 output_mode 的返回字段集合完全一致（表驱动）。
func TestExecFieldSetIdenticalAcrossModes(t *testing.T) {
	s, _ := newSpillTestServer(t)
	want := []string{"device", "duration_ms", "exit_code", "sha256", "spill_device", "spill_path", "stderr", "stdout", "total_bytes", "total_lines", "truncated"}
	for _, tc := range []struct {
		name, mode, out string
	}{
		{"raw-truncated", "raw", strings.Repeat("x", 9000)},
		{"head-truncated", "head", strings.Repeat("x", 9000)},
		{"tail-truncated", "tail", strings.Repeat("x", 9000)},
		{"count-truncated", "count", strings.Repeat("x", 9000)},
		{"none-truncated", "none", strings.Repeat("x", 9000)},
		{"raw-small", "raw", "ok"},
		{"count-small", "count", "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := s.execResponse("tok-A", "local", 0, tc.out, "", 1, executor.OutputOptions{Mode: tc.mode, MaxBytes: 4096})
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for k := range resp {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != strings.Join(want, ",") {
				t.Fatalf("keys = %v, want %v", keys, want)
			}
		})
	}
}

// 溢出目录 0700，溢出文件 0600。
func TestSpillFilePermissions(t *testing.T) {
	s, dbPath := newSpillTestServer(t)
	resp, err := s.execResponse("tok-A", "local", 0, strings.Repeat("p", 9000), "", 1, executor.OutputOptions{Mode: "raw", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(dbPath), spillDirName)
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	fi, err := os.Stat(resp["spill_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// 溢出目录位于数据库文件旁边（数据目录）。
func TestSpillDirectoryIsNextToDatabase(t *testing.T) {
	s, dbPath := newSpillTestServer(t)
	st := s.spillFor()
	if st == nil {
		t.Fatal("spill store unavailable for a file-backed database")
	}
	if st.dir != mustResolve(t, filepath.Join(filepath.Dir(dbPath), spillDirName)) {
		t.Fatalf("spill dir = %s", st.dir)
	}
}

// 过期（TTL 24 小时）后读取返回 errSpillNotFound，文件被删除。
func TestSpillExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	st, err := newSpillStore(filepath.Join(t.TempDir(), "mcp-spill"), 1<<20, 24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	path, err := st.write("tok-A", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.read("tok-A", path, 0, 100); err != nil {
		t.Fatalf("read before TTL: %v", err)
	}
	now = now.Add(25 * time.Hour)
	if _, err := st.read("tok-A", path, 0, 100); !errors.Is(err, errSpillNotFound) {
		t.Fatalf("read after TTL: err = %v, want errSpillNotFound", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired file still on disk: %v", err)
	}
}

// 总量超过上限时先淘汰最旧的文件。
func TestSpillEvictsOldestWhenCapReached(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	st, err := newSpillStore(filepath.Join(t.TempDir(), "mcp-spill"), 10, 24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.write("tok-A", []byte("aaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	b, err := st.write("tok-A", []byte("bbbbbb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("oldest file not evicted: %v", err)
	}
	if _, err := st.read("tok-A", a, 0, 10); !errors.Is(err, errSpillNotFound) {
		t.Fatalf("evicted read: %v", err)
	}
	if c, err := st.read("tok-A", b, 0, 10); err != nil || c.Content != "bbbbbb" {
		t.Fatalf("newest read = %q, %v", c.Content, err)
	}
}

// 其他令牌读取、不存在的文件、已过期的文件，返回同一个错误（错误文本逐字相同）。
func TestSpillOtherTokenAndMissingFileGiveSameError(t *testing.T) {
	st, err := newSpillStore(filepath.Join(t.TempDir(), "mcp-spill"), 1<<20, 24*time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := st.write("tok-A", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	_, errOther := st.read("tok-B", owned, 0, 10)
	_, errMissing := st.read("tok-B", filepath.Join(st.dir, "00000000000000000000000000000000.out"), 0, 10)
	if !errors.Is(errOther, errSpillNotFound) || !errors.Is(errMissing, errSpillNotFound) {
		t.Fatalf("errors = %v / %v", errOther, errMissing)
	}
	if errOther.Error() != errMissing.Error() {
		t.Fatalf("different messages reveal state: %q vs %q", errOther, errMissing)
	}
}

// 用 .. 或符号链接跳出溢出目录，被拒绝（与不存在的文件返回同一个错误）。
func TestSpillRejectsPathEscapes(t *testing.T) {
	st, err := newSpillStore(filepath.Join(t.TempDir(), "mcp-spill"), 1<<20, 24*time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := st.write("tok-A", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	dotdot := filepath.Join(st.dir, "..", filepath.Base(owned))
	if _, err := st.read("tok-A", dotdot, 0, 10); !errors.Is(err, errSpillNotFound) {
		t.Fatalf("../ escape: err = %v", err)
	}
	link := filepath.Join(t.TempDir(), "evil")
	if err := os.Symlink(owned, link); err != nil {
		t.Fatal(err)
	}
	if _, err := st.read("tok-A", link, 0, 10); !errors.Is(err, errSpillNotFound) {
		t.Fatalf("symlink escape: err = %v", err)
	}
}

// read_file 经 MCP 访问溢出设备：设备白名单不包含 Hub 时仍能读取自己的溢出文件；
// 其他令牌得到相同错误；exec 的 structuredContent 与 text 一致且字段统一。
func TestMCPReadFileServesSpillAcrossDeviceWhitelist(t *testing.T) {
	s, _ := newSpillTestServer(t)
	srv := mcp.NewServer(s.NewMCPBackend(), nil)
	srv.SetLocalName("local")
	call := func(ctx context.Context, name string, args map[string]any) mcp.JSONRPCResponse {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		return srv.HandleDirectRequest(ctx, mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	}
	owner := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "tok-A", Name: "tok-A", TokenID: "tok-A", Scopes: map[string]bool{"exec": true, "files:read": true}})
	resp := call(owner, "exec_command", map[string]any{"command": "head -c 5000 /dev/zero | tr '\\0' a", "output_mode": "raw", "max_bytes": 1024})
	res, ok := resp.Result.(mcp.CallToolResult)
	if !ok || res.IsError {
		t.Fatalf("exec failed: %#v", resp)
	}
	structured, ok := res.StructuredContent.(map[string]any)
	if !ok || len(structured) != 11 {
		t.Fatalf("structuredContent = %#v", res.StructuredContent)
	}
	var textObj map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &textObj); err != nil || textObj["spill_path"] != structured["spill_path"] {
		t.Fatalf("text and structuredContent disagree: %v", err)
	}
	spill := structured["spill_path"].(string)

	reader := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "tok-A", Name: "tok-A", TokenID: "tok-A", Scopes: map[string]bool{"files:read": true}, Devices: map[string]bool{"node-a": true}})
	rd := call(reader, "read_file", map[string]any{"device": mcp.SpillDeviceName, "path": spill, "offset": 0, "limit": 64})
	rr, ok := rd.Result.(mcp.CallToolResult)
	if !ok || rr.IsError {
		t.Fatalf("owner read blocked by device whitelist: %#v", rd)
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(rr.Content[0].Text), &page); err != nil || page["total_size"] != float64(5000) || page["bytes"] != float64(64) {
		t.Fatalf("page = %v (%v)", page, err)
	}

	intruder := auth.WithPrincipal(context.Background(), auth.Principal{Type: "api_token", ID: "tok-B", Name: "tok-B", TokenID: "tok-B", Scopes: map[string]bool{"files:read": true}})
	bad := call(intruder, "read_file", map[string]any{"device": mcp.SpillDeviceName, "path": spill, "offset": 0, "limit": 64})
	br, _ := bad.Result.(mcp.CallToolResult)
	if !br.IsError || !strings.Contains(br.Content[0].Text, errSpillNotFound.Error()) {
		t.Fatalf("other token read not refused: %#v", br)
	}
	_ = time.Second
}
