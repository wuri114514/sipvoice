// Package configbundle 提供「整机配置打包 / 导入导出」能力。
//
// 设计目标（也是后续新增功能必须遵守的约定）：
//
//  1. 与程序版本无关：包内只有 format 与 format_version，
//     不依赖 main.version。读取端只要求 format_version <= 当前实现版本，
//     因此老包永远能被新程序读进来。
//
//  2. 向后兼容：区段（section）是可选的。缺哪个区段就跳过哪个区段，
//     其余照常导入；区段内缺失的字段由各自结构的默认值与 Normalize 补齐。
//
//  3. 向前兼容（关键）：遇到本版本不认识的区段，不报错、不丢弃，
//     原样写进 /etc/sipvoice/config-bundle.extra.json 作为旁挂数据；
//     下次导出会自动并回包里，实现往返无损。
//     以后新增功能只要往 sections 里加一个新 key 即可，老程序照样能搬运。
//
//  4. 尽力而为：单个区段解析失败不影响其它区段，错误逐条记进报告。
//
// 包格式（JSON）：
//
//	{
//	  "format": "sipvoice.config-bundle",
//	  "format_version": 1,
//	  "exported_at": "2026-11-27T20:00:00Z",
//	  "producer": { "app_version": "2.1.12", "hostname": "...", "os": "..." },
//	  "sections": {
//	    "config":  { ... },   // /etc/sipvoice/config.json
//	    "fax":     { ... },   // fax.json
//	    "mail":    { ... },   // mail.json
//	    "phones":  [ ... ],   // phones.json
//	    "users":   [ ... ],   // users.json 里的 users 数组
//	    "secrets": { ... }    // 解密后的密钥（需显式选择包含）
//	  }
//	}
package configbundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"sipvoice/internal/config"
	"sipvoice/internal/fax"
	"sipvoice/internal/mail"
	"sipvoice/internal/phones"
)

const (
	// Format 是包格式标识，写入包内用于识别。
	Format = "sipvoice.config-bundle"
	// FormatVersion 是本实现能写出的包格式版本。
	// 新增功能若只是加区段，不必提升它；只有改变已有区段的语义时才提升。
	FormatVersion = 1
)

// 区段名。以后新增功能在此追加常量与对应导入导出分支即可。
const (
	SectionConfig  = "config"
	SectionFax     = "fax"
	SectionMail    = "mail"
	SectionPhones  = "phones"
	SectionUsers   = "users"
	SectionSecrets = "secrets"
)

// knownSections 是本实现认识的区段，顺序即导入顺序。
var knownSections = []string{
	SectionConfig, SectionSecrets, SectionFax, SectionMail, SectionPhones, SectionUsers,
}

// Producer 记录产出方信息，仅供人看，不参与兼容判断。
type Producer struct {
	AppVersion string `json:"app_version"`
	Hostname   string `json:"hostname,omitempty"`
	OS         string `json:"os,omitempty"`
}

// Bundle 是整包结构。
type Bundle struct {
	Format        string                     `json:"format"`
	FormatVersion int                        `json:"format_version"`
	ExportedAt    string                     `json:"exported_at"`
	Producer      Producer                   `json:"producer"`
	Sections      map[string]json.RawMessage `json:"sections"`
}

// ExportOptions 控制导出内容。
type ExportOptions struct {
	// Dir 是配置目录，通常是 /etc/sipvoice。
	Dir string
	// AppVersion 仅写入 producer 作参考。
	AppVersion string
	// IncludeSecrets 为 true 时把密钥解密后放进包里。
	// 默认不包含：包一旦外流就等于交出全部云端账号。
	IncludeSecrets bool
}

// SectionResult 是单个区段的处理结果。
type SectionResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // applied / skipped / failed
	Items  int    `json:"items,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Report 是一次导入的结果。
type Report struct {
	OK        bool            `json:"ok"`
	DryRun    bool            `json:"dry_run"`
	Sections  []SectionResult `json:"sections"`
	Unknown   []string        `json:"unknown,omitempty"`
	BackupDir string          `json:"backup_dir,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// ImportOptions 控制导入行为。
type ImportOptions struct {
	Dir     string
	DryRun  bool
	AppPath string // 仅用于报告展示
}

// extraPath 是「本版本不认识的区段」的旁挂文件。
// 目的是让未来版本新增的区段在旧程序上往返一次也不丢失。
func extraPath(dir string) string {
	return filepath.Join(dir, "config-bundle.extra.json")
}

// Export 从配置目录读取全部已知区段，组装成包。
func Export(opts ExportOptions) (*Bundle, error) {
	dir := opts.Dir
	if dir == "" {
		return nil, fmt.Errorf("配置目录为空")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("配置目录不可读: %w", err)
	}

	b := &Bundle{
		Format:        Format,
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Producer: Producer{
			AppVersion: opts.AppVersion,
			OS:         runtime.GOOS + "/" + runtime.GOARCH,
		},
		Sections: map[string]json.RawMessage{},
	}
	if h, err := os.Hostname(); err == nil {
		b.Producer.Hostname = h
	}

	// config.json
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			// 导出时剥掉明文密钥，密钥统一走 secrets 区段
			if c, err := config.Load(filepath.Join(dir, "config.json")); err == nil {
				scrubbed := *c
				config.ScrubSecrets(&scrubbed)
				if out, err := json.Marshal(scrubbed); err == nil {
					b.Sections[SectionConfig] = out
				} else {
					b.Sections[SectionConfig] = raw
				}
			} else {
				b.Sections[SectionConfig] = raw
			}
		}
	}
	// 兜底：上面没取到就原样放
	if _, ok := b.Sections[SectionConfig]; !ok {
		if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
			b.Sections[SectionConfig] = raw
		}
	}

	// secrets.json（解密后以明文形态入包）
	if opts.IncludeSecrets {
		if sec, err := config.LoadSecrets(filepath.Join(dir, "config.json")); err == nil && sec != nil {
			if out, err := json.Marshal(sec); err == nil {
				b.Sections[SectionSecrets] = out
			}
		}
	}

	// fax.json
	if raw, err := os.ReadFile(filepath.Join(dir, "fax.json")); err == nil {
		b.Sections[SectionFax] = raw
	}

	// mail.json
	if raw, err := os.ReadFile(filepath.Join(dir, "mail.json")); err == nil {
		b.Sections[SectionMail] = raw
	}

	// phones.json
	if raw, err := os.ReadFile(filepath.Join(dir, "phones.json")); err == nil {
		b.Sections[SectionPhones] = raw
	}

	// users.json —— 只导 users 数组，Salt/Hash 是 PBKDF2 结果，跨机可用
	if raw, err := os.ReadFile(filepath.Join(dir, "users.json")); err == nil {
		var d struct {
			Users []map[string]any `json:"users"`
		}
		if err := json.Unmarshal(raw, &d); err == nil && len(d.Users) > 0 {
			if out, err := json.Marshal(d.Users); err == nil {
				b.Sections[SectionUsers] = out
			}
		}
	}

	// 并回旁挂的未知区段（来自未来版本导入过的包）
	if raw, err := os.ReadFile(extraPath(dir)); err == nil {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(raw, &extra); err == nil {
			for k, v := range extra {
				if _, exists := b.Sections[k]; !exists {
					b.Sections[k] = v
				}
			}
		}
	}

	return b, nil
}

// Parse 校验并解析一个包。
func Parse(data []byte) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("不是合法的配置包: %w", err)
	}
	if b.Format == "" {
		// 兼容：没有 format 字段但看起来像配置包（有 sections）也接受
		if len(b.Sections) == 0 {
			return nil, fmt.Errorf("不是 SIPVoice 配置包（缺少 format 与 sections）")
		}
		b.Format = Format
	}
	if b.Format != Format {
		return nil, fmt.Errorf("格式不匹配：期望 %q，实际 %q", Format, b.Format)
	}
	if b.FormatVersion <= 0 {
		b.FormatVersion = 1 // 早期手写包兜底
	}
	if b.FormatVersion > FormatVersion {
		return nil, fmt.Errorf("这个包由更新版本的程序生成（format_version=%d，本程序最高支持 %d），请先升级",
			b.FormatVersion, FormatVersion)
	}
	return &b, nil
}

// Import 把一个包应用到配置目录。单个区段失败不影响其余区段。
func Import(b *Bundle, opts ImportOptions) (*Report, error) {
	dir := opts.Dir
	if dir == "" {
		return nil, fmt.Errorf("配置目录为空")
	}
	if b == nil {
		return nil, fmt.Errorf("空配置包")
	}
	rep := &Report{OK: true, DryRun: opts.DryRun}
	if b.FormatVersion < FormatVersion {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("包格式为第 %d 版，已按向后兼容规则导入", b.FormatVersion))
	}

	// 导入前整目录备份，出错可手工回滚
	if !opts.DryRun {
		if bk, err := backupDir(dir); err == nil && bk != "" {
			rep.BackupDir = bk
		} else if err != nil {
			rep.Warnings = append(rep.Warnings, "导入前备份失败: "+err.Error())
		}
	}

	// 逐区段导入
	for _, name := range knownSections {
		raw, ok := b.Sections[name]
		if !ok {
			rep.Sections = append(rep.Sections, SectionResult{Name: name, Status: "skipped", Note: "包里没有这个区段"})
			continue
		}
		res := importSection(dir, name, raw, opts.DryRun)
		rep.Sections = append(rep.Sections, res)
		if res.Status == "failed" {
			rep.OK = false
		}
	}

	// 未知区段：旁挂保存，保证往返不丢
	var unknown []string
	extraMap := map[string]json.RawMessage{}
	for k, v := range b.Sections {
		if isKnown(k) {
			continue
		}
		unknown = append(unknown, k)
		extraMap[k] = v
	}
	sort.Strings(unknown)
	rep.Unknown = unknown
	if len(extraMap) > 0 && !opts.DryRun {
		if out, err := json.MarshalIndent(extraMap, "", "  "); err == nil {
			_ = os.WriteFile(extraPath(dir), out, 0o600)
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("有 %d 个本版本不认识的区段（%s），已原样保存；升级后导入包时会自动带上",
					len(unknown), strings.Join(unknown, ", ")))
		}
	}

	return rep, nil
}

func isKnown(name string) bool {
	for _, k := range knownSections {
		if k == name {
			return true
		}
	}
	return false
}

func importSection(dir, name string, raw json.RawMessage, dryRun bool) SectionResult {
	res := SectionResult{Name: name, Status: "applied"}

	switch name {
	case SectionConfig:
		var c config.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		p := filepath.Join(dir, "config.json")
		if cur, err := config.Load(p); err == nil {
			// 保留当前密钥，避免导入不含密钥的包时把云端账号清空
			config.ApplySecrets(&c, config.SecretsFromConfig(cur))
		}
		if err := config.Set(&c); err != nil {
			res.Status, res.Note = "failed", "写入失败: "+err.Error()
		}
		return res

	case SectionSecrets:
		var s config.Secrets
		if err := json.Unmarshal(raw, &s); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		if err := config.SaveSecrets(filepath.Join(dir, "config.json"), &s); err != nil {
			res.Status, res.Note = "failed", "保存失败: "+err.Error()
		} else {
			res.Note = "密钥已按本机重新加密"
		}
		return res

	case SectionFax:
		var fc fax.Config
		if err := json.Unmarshal(raw, &fc); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		fc.Normalize()
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		if err := fax.SaveTo(filepath.Join(dir, "fax.json"), fc); err != nil {
			res.Status, res.Note = "failed", "写入失败: "+err.Error()
		}
		return res

	case SectionMail:
		var ms mail.Settings
		if err := json.Unmarshal(raw, &ms); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		st := &mail.Store{Path: filepath.Join(dir, "mail.json")}
		// 保留轮询状态（本机运行时数据，不该被导入覆盖）
		cur := st.Load()
		if ms.Receive.LastPoll == "" {
			ms.Receive.LastPoll = cur.Receive.LastPoll
			ms.Receive.LastResult = cur.Receive.LastResult
		}
		if ms.LastTest == "" {
			ms.LastTest = cur.LastTest
			ms.LastOK = cur.LastOK
		}
		if err := st.Save(ms); err != nil {
			res.Status, res.Note = "failed", "写入失败: "+err.Error()
		}
		return res

	case SectionPhones:
		var list []phones.Phone
		if err := json.Unmarshal(raw, &list); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		res.Items = len(list)
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		ps := phones.New(filepath.Join(dir, "phones.json"))
		if err := ps.Replace(list); err != nil {
			res.Status, res.Note = "failed", "写入失败: "+err.Error()
		}
		return res

	case SectionUsers:
		// 只认 users 数组；账号密码是 PBKDF2 结果，可直接搬到别的机器
		var users []map[string]any
		if err := json.Unmarshal(raw, &users); err != nil {
			res.Status, res.Note = "failed", "解析失败: "+err.Error()
			return res
		}
		res.Items = len(users)
		if dryRun {
			res.Note = "干跑：已通过解析"
			return res
		}
		p := filepath.Join(dir, "users.json")
		if existing, err := os.ReadFile(p); err == nil {
			var cur struct {
				Users []map[string]any `json:"users"`
			}
			_ = json.Unmarshal(existing, &cur)
			if len(cur.Users) > 0 {
				// 不覆盖已有账号，避免把当前登录的人踢掉
				res.Note = fmt.Sprintf("已有 %d 个账号，未覆盖", len(cur.Users))
				res.Status = "skipped"
				return res
			}
		}
		out, err := json.MarshalIndent(map[string]any{"users": users}, "", "  ")
		if err != nil {
			res.Status, res.Note = "failed", err.Error()
			return res
		}
		if err := os.WriteFile(p, out, 0o600); err != nil {
			res.Status, res.Note = "failed", "写入失败: "+err.Error()
		}
	}

	return res
}

// backupDir 把配置目录里的 json 复制到 backups/pre-import-<时间戳>/。
func backupDir(dir string) (string, error) {
	src := dir
	dst := filepath.Join(dir, "backups", "pre-import-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o600); err != nil {
			return "", err
		}
		n++
	}
	if n == 0 {
		return "", nil
	}
	return dst, nil
}
