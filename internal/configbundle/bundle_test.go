package configbundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTempDir 造一个带基础配置文件的临时目录。
func newTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"config.json": `{"ai":{"chat_model":"m1","voice":"v1"},"agent":{"silence_ms":1234}}`,
		"fax.json":    `{"enabled":true,"inbound_ext":"600","prompt_wait":25}`,
		"mail.json":   `{"enabled":true,"notify_to":"a@example.com","primary":{"host":"smtp.example.com","port":465,"username":"u"}}`,
		"phones.json": `[{"number":"1001","name":"前台","password":"p1","enabled":true}]`,
		"users.json":  `{"users":[{"name":"admin","salt":"s","hash":"h","iter":1000}]}`,
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatalf("写 %s: %v", n, err)
		}
	}
	return dir
}

func TestExportImportRoundTrip(t *testing.T) {
	src := newTempDir(t)

	b, err := Export(ExportOptions{Dir: src, AppVersion: "9.9.9"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if b.Format != Format || b.FormatVersion != FormatVersion {
		t.Fatalf("包标识不对: %s/%d", b.Format, b.FormatVersion)
	}
	for _, k := range []string{SectionConfig, SectionFax, SectionMail, SectionPhones, SectionUsers} {
		if _, ok := b.Sections[k]; !ok {
			t.Errorf("缺少区段 %s", k)
		}
	}

	// 序列化 → 解析 → 导入到一个空目录
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	pb, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	dst := t.TempDir()
	rep, err := Import(pb, ImportOptions{Dir: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !rep.OK {
		t.Fatalf("报告不 OK: %+v", rep)
	}

	// 关键值必须落到位
	cfgRaw, err := os.ReadFile(filepath.Join(dst, "config.json"))
	if err != nil {
		t.Fatalf("读回 config.json: %v", err)
	}
	if !strings.Contains(string(cfgRaw), "m1") {
		t.Errorf("config 未导入: %s", cfgRaw)
	}

	phRaw, _ := os.ReadFile(filepath.Join(dst, "phones.json"))
	if !strings.Contains(string(phRaw), "1001") {
		t.Errorf("phones 未导入: %s", phRaw)
	}

	mailRaw, _ := os.ReadFile(filepath.Join(dst, "mail.json"))
	if !strings.Contains(string(mailRaw), "smtp.example.com") {
		t.Errorf("mail 未导入: %s", mailRaw)
	}

	// 导入前应有备份目录
	if rep.BackupDir == "" {
		t.Log("提示：目标目录原本为空，无备份目录（可接受）")
	}
}

// 关健：未来版本新增的未知区段必须原样保留，并在下一次导出时带回。
func TestUnknownSectionPreserved(t *testing.T) {
	dst := t.TempDir()

	b := &Bundle{
		Format:        Format,
		FormatVersion: FormatVersion,
		Sections: map[string]json.RawMessage{
			SectionFax: json.RawMessage(`{"enabled":true}`),
			// 假装是 2.2.0 才有的新功能区段
			"vision_advanced": json.RawMessage(`{"threshold":0.85,"model":"x"}`),
			"future_thing":    json.RawMessage(`[1,2,3]`),
		},
	}
	rep, err := Import(b, ImportOptions{Dir: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(rep.Unknown) != 2 {
		t.Fatalf("未知区段应记录 2 个，实际 %v", rep.Unknown)
	}

	// 旁挂文件应存在
	if _, err := os.Stat(extraPath(dst)); err != nil {
		t.Fatalf("未知区段旁挂文件未生成: %v", err)
	}

	// 再导出：未知区段要回来
	b2, err := Export(ExportOptions{Dir: dst})
	if err != nil {
		t.Fatalf("再导出: %v", err)
	}
	for _, k := range []string{"vision_advanced", "future_thing"} {
		if _, ok := b2.Sections[k]; !ok {
			t.Errorf("往返后丢失未知区段 %s", k)
		}
	}
}

// 高版本包应被明确拒绝，而不是悄悄按老规则导入。
func TestRejectNewerFormat(t *testing.T) {
	raw := `{"format":"sipvoice.config-bundle","format_version":99,"sections":{}}`
	if _, err := Parse([]byte(raw)); err == nil {
		t.Fatal("高版本包应报错")
	} else if !strings.Contains(err.Error(), "升级") {
		t.Errorf("错误信息应提示升级，实际: %v", err)
	}
}

// 缺 format 但结构像包的（早期手写）应被接受。
func TestParseLenientOlderShape(t *testing.T) {
	raw := `{"format_version":1,"sections":{"fax":{"enabled":true}}}`
	b, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("应接受缺 format 的包: %v", err)
	}
	if b.Format != Format {
		t.Errorf("format 应被补成 %s，实际 %s", Format, b.Format)
	}
}

// 区段缺失时跳过而不是整包失败。
func TestMissingSectionsTolerated(t *testing.T) {
	dst := t.TempDir()
	b := &Bundle{
		Format:        Format,
		FormatVersion: FormatVersion,
		Sections:      map[string]json.RawMessage{SectionFax: json.RawMessage(`{"enabled":true}`)},
	}
	rep, err := Import(b, ImportOptions{Dir: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !rep.OK {
		t.Errorf("缺区段不该判失败: %+v", rep)
	}
	skipped := 0
	for _, s := range rep.Sections {
		if s.Status == "skipped" {
			skipped++
		}
	}
	if skipped < 4 {
		t.Errorf("应有多个区段被跳过，实际 %d", skipped)
	}
}

// 干跑不应写任何文件。
func TestDryRunWritesNothing(t *testing.T) {
	dst := t.TempDir()
	b := &Bundle{
		Format:        Format,
		FormatVersion: FormatVersion,
		Sections:      map[string]json.RawMessage{SectionFax: json.RawMessage(`{"enabled":true}`)},
	}
	rep, err := Import(b, ImportOptions{Dir: dst, DryRun: true})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !rep.DryRun {
		t.Error("报告应标记为干跑")
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("干跑却写了文件: %v", names)
	}
}

// 未选择包含密钥时，包里不应出现 secrets 区段。
func TestExportWithoutSecrets(t *testing.T) {
	src := newTempDir(t)
	b, err := Export(ExportOptions{Dir: src, IncludeSecrets: false})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, ok := b.Sections[SectionSecrets]; ok {
		t.Error("未要求包含密钥时不应有 secrets 区段")
	}
}

// 已有账号时，导入不应覆盖（避免把当前登录的人踢掉）。
func TestUsersNotOverwritten(t *testing.T) {
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "users.json"),
		[]byte(`{"users":[{"name":"me","salt":"s","hash":"h","iter":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Bundle{
		Format:        Format,
		FormatVersion: FormatVersion,
		Sections: map[string]json.RawMessage{
			SectionUsers: json.RawMessage(`[{"name":"other","salt":"x","hash":"y","iter":1}]`),
		},
	}
	rep, err := Import(b, ImportOptions{Dir: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "users.json"))
	if strings.Contains(string(got), "other") {
		t.Error("已有账号不应被覆盖")
	}
	var st string
	for _, s := range rep.Sections {
		if s.Name == SectionUsers {
			st = s.Status
		}
	}
	if st != "skipped" {
		t.Errorf("users 区段状态应为 skipped，实际 %q", st)
	}
}
