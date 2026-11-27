package mail

import (
	"os"
	"path/filepath"
	"testing"
)

// mail.json 可能是 install 时由 root 建的，服务以 sipvoice 身份保存时
// 必须靠「目录内建临时文件 + rename」才能成功；顺带确认不留垃圾文件。
func TestStoreSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mail.json")
	st := &Store{Path: p}

	s := DefaultSettings()
	s.Receive.Enabled = true
	s.Receive.Host = "imap.example.com"
	s.Receive.Username = "fax@example.com"
	s.Receive.Password = "code"
	s.Receive.FromAllow = []string{"Boss@Example.com", "boss@example.com"}
	if err := st.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := st.Load()
	if !got.Receive.Enabled || got.Receive.Host != "imap.example.com" {
		t.Errorf("读回来的收信配置不对: %+v", got.Receive)
	}
	if len(got.Receive.FromAllow) != 1 || got.Receive.FromAllow[0] != "boss@example.com" {
		t.Errorf("白名单应去重并转小写: %v", got.Receive.FromAllow)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "mail.json" {
		t.Fatalf("目录里应只有 mail.json，实际 %v", names)
	}
}

func TestReceiveDefaultsNormalized(t *testing.T) {
	r := DefaultReceiveConfig()
	r.Port = 0
	r.PollSeconds = 1
	r.MaxAttachMB = 999
	r.Mailbox = "  "
	r.SubjectTag = ""
	r.Normalize()

	if r.Port != 993 || r.PollSeconds != defaultPollSeconds || r.MaxAttachMB != 50 {
		t.Errorf("默认值不对: %+v", r)
	}
	if r.Mailbox != DefaultMailbox || r.SubjectTag != DefaultSubjectTag {
		t.Errorf("空字段没回填: %+v", r)
	}
}
