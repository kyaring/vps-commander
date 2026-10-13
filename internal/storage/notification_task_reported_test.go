package storage

import (
	"path/filepath"
	"testing"
)

func TestTaskReportedBuiltinTemplateSeedAndPreserveEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-reported.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	template, err := st.GetNotificationTemplate("task-reported")
	if err != nil {
		t.Fatal(err)
	}
	if template.Name != "任务回报" || template.EventType != "task.reported" || template.Severity != "P3" || template.Format != "text" || template.TitleTemplate != "【{{employee}}】{{type}}{{status}}：{{project}}" || template.BodyTemplate != "任务：{{task}}\n关联：{{ref}}\n摘要：{{summary}}" || template.VariablesJSON != `["employee","project","task","type","status","ref","summary"]` || !template.Enabled {
		t.Fatalf("unexpected task-reported template: %+v", template)
	}
	if _, err := st.DB.Exec(`UPDATE notification_templates SET name='custom task report',title_template='custom title' WHERE id='task-reported'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	template, err = st.GetNotificationTemplate("task-reported")
	if err != nil {
		t.Fatal(err)
	}
	if template.Name != "custom task report" || template.TitleTemplate != "custom title" {
		t.Fatalf("migration overwrote customized template: %+v", template)
	}
}
