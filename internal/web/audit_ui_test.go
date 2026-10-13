package web

import (
	"html"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestAuditUIPanelColumnsAndRowTemplate(t *testing.T) {
	indexBytes, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexBytes)
	bodyStart := strings.Index(index, `<tbody id="audits"`)
	if bodyStart < 0 {
		t.Fatal(`index.html does not contain tbody id="audits"`)
	}
	theadStart := strings.LastIndex(index[:bodyStart], "<thead")
	if theadStart < 0 {
		t.Fatal("could not find the audit table's thead immediately before tbody id=audits")
	}
	theadEndRel := strings.Index(index[theadStart:bodyStart], "</thead>")
	if theadEndRel < 0 {
		t.Fatal("could not find closing thead before tbody id=audits")
	}
	theadEnd := theadStart + theadEndRel + len("</thead>")
	if theadStart >= bodyStart || theadEnd > bodyStart {
		t.Fatal("audit table thead is not before tbody id=audits")
	}
	thead := index[theadStart:theadEnd]
	thRE := regexp.MustCompile(`(?s)<th(?:\s[^>]*)?>(.*?)</th>`)
	tagRE := regexp.MustCompile(`<[^>]*>`)
	matches := thRE.FindAllStringSubmatch(thead, -1)
	want := []string{"时间", "令牌", "传输", "设备", "动作", "命令/路径", "退出码", "耗时", "结果", "来源 IP"}
	if len(matches) != len(want) {
		t.Fatalf("audit table has %d header columns, want %d: %q", len(matches), len(want), matches)
	}
	for i, match := range matches {
		got := strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(match[1], "")))
		if got != want[i] {
			t.Errorf("audit header column %d = %q, want %q", i+1, got, want[i])
		}
	}

	jsBytes, err := staticFS.ReadFile("static/audit-ui.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	functionStart := strings.Index(js, "function renderAuditRow")
	if functionStart < 0 {
		t.Fatal("audit-ui.js does not define renderAuditRow")
	}
	returnStartRel := strings.Index(js[functionStart:], "return '")
	if returnStartRel < 0 {
		t.Fatal("renderAuditRow does not return a quoted row template")
	}
	returnStart := functionStart + returnStartRel
	lineEndRel := strings.IndexByte(js[returnStart:], '\n')
	if lineEndRel < 0 {
		lineEndRel = len(js) - returnStart
	}
	rowTemplate := js[returnStart : returnStart+lineEndRel]
	if got := strings.Count(rowTemplate, "<td"); got != len(want) {
		t.Errorf("renderAuditRow template has %d <td occurrences, want %d", got, len(want))
	}
	if !strings.Contains(index, `src="/static/audit-ui.js"`) {
		t.Error(`index.html does not reference /static/audit-ui.js`)
	}
	if !strings.Contains(js, "escapeHTML(row.transport") {
		t.Error("audit-ui.js does not escape row.transport")
	}
	if !strings.Contains(js, "audit-result") {
		t.Error(`audit-ui.js does not render the audit-result class`)
	}
}

func TestAuditUINodeRegression(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node executable is not on PATH; JavaScript regression test cannot run on this node")
	}
	cmd := exec.Command(node, "testdata/audit-ui.test.js")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node testdata/audit-ui.test.js failed: %v\n%s", err, output)
	}
}
