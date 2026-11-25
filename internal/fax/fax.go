package fax

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/mail"
)

// Config 是传真相关设置（面板可改）。
type Config struct {
	Enabled     bool   `json:"enabled"`      // 是否启用传真
	InboundExt  string `json:"inbound_ext"`  // 接收传真分机号，如 600
	Spool       string `json:"spool"`        // 传真存档目录
	ECM         bool   `json:"ecm"`          // 纠错模式；公网 T.38 建议关闭
	MaxPages    int    `json:"max_pages"`    // 单份最多接收页数
	HangupDelay int    `json:"hangup_delay"` // 传真结束后等待秒数（给对方发 DCN）
	SendContext string `json:"send_context"` // 内部发送上下文名

	// 发送前的语音提示（预生成一次，反复使用，避免每次调用云端 TTS 计费）
	PromptEnabled bool   `json:"prompt_enabled"`
	PromptText    string `json:"prompt_text"`
	PromptWait    int    `json:"prompt_wait"`
	PromptFile    string `json:"prompt_file"`

	// 发送失败/用户拒绝时的处理
	FailDir      string `json:"fail_dir"`
	NotifyEmail  bool   `json:"notify_email"`
	MailTo       string `json:"mail_to"`
	NoFaxMessage string `json:"no_fax_message"`
}

// DefaultConfig 返回默认传真设置。
func DefaultConfig() Config {
	return Config{
		Enabled:     true,
		InboundExt:  "600",
		Spool:       "/var/spool/asterisk/fax",
		ECM:         false,
		MaxPages:    50,
		HangupDelay: 3,
		SendContext: "sipfax",

		PromptEnabled: true,
		PromptText:    "感谢接听，这是 AI 下发的传真服务。请按传真机上的开始键接收。如果无法接收传真，请按 404 结束。",
		PromptWait:    30,
		PromptFile:    "/var/lib/sipvoice/prompts/fax-intro.wav",

		FailDir:      "/var/lib/sipvoice/fax-failed",
		NotifyEmail:  false,
		MailTo:       "",
		NoFaxMessage: "抱歉没收到传真的握手信号，本次传真将以其他方式送到，再见。",
	}
}

// Normalize 补齐空值。
func (c *Config) Normalize() {
	d := DefaultConfig()
	if c.InboundExt == "" {
		c.InboundExt = d.InboundExt
	}
	if c.Spool == "" {
		c.Spool = d.Spool
	}
	if c.MaxPages <= 0 {
		c.MaxPages = d.MaxPages
	}
	if c.HangupDelay < 0 {
		c.HangupDelay = 0
	}
	if c.SendContext == "" {
		c.SendContext = d.SendContext
	}
	if c.PromptWait <= 0 {
		c.PromptWait = d.PromptWait
	}
	if c.PromptFile == "" {
		c.PromptFile = d.PromptFile
	}
	if c.PromptText == "" {
		c.PromptText = d.PromptText
	}
	if c.FailDir == "" {
		c.FailDir = d.FailDir
	}
	if c.NoFaxMessage == "" {
		c.NoFaxMessage = d.NoFaxMessage
	}
}

// Item 是一份收到的传真。
type Item struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	Pages   int    `json:"pages"`
	Caller  string `json:"caller"`
	Preview bool   `json:"preview"`
	// Summary 是 AI 对这份传真的分析摘要（没有分析过则空）
	Summary string `json:"summary,omitempty"`
}

// 发送状态
type sendState struct {
	mu       sync.Mutex
	running  bool
	target   string
	file     string
	started  time.Time
	finished time.Time
	ok       bool
	message  string
}

var (
	state   = &sendState{}
	logPath = "/var/log/asterisk/messages.log"
)

// astCLI 执行 Asterisk CLI（优先 sudo）。
func astCLI(args ...string) (string, error) {
	line := strings.Join(args, " ")
	cmds := [][]string{
		{"sudo", "-n", "/usr/sbin/asterisk", "-rx", line},
		{"/usr/sbin/asterisk", "-rx", line},
	}
	var lastErr error
	for _, c := range cmds {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
		lastErr = fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return "", lastErr
}

// Load 读取传真配置。
func Load() Config {
	cfg := DefaultConfig()
	raw, err := os.ReadFile("/etc/sipvoice/fax.json")
	if err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	cfg.Normalize()
	return cfg
}

// Save 保存传真配置。
func Save(c Config) error {
	c.Normalize()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("/etc/sipvoice/fax.json", raw, 0o660)
}

// ApplyDialplan 生成传真发送用的拨号方案并 reload。
// 接收分机（默认 600）由 phones 模板的 [internal] 负责，这里不再重复定义，
// 否则会出现 "extension already in use" 冲突。
func ApplyDialplan(c Config) error {
	path := "/etc/asterisk/extensions_sipfax.conf"
	ensureInclude()
	var b strings.Builder

	b.WriteString("; 由 SIPVoice 控制台生成——传真发送拨号方案\n\n")
	b.WriteString("[fax-send]\n")
	b.WriteString("exten => reply,1,NoOp(SIPVoice fax send to ${DB(sipvoice/faxdest)})\n")
	b.WriteString(" same => n,Answer()\n")

	if c.PromptEnabled {
		b.WriteString(" same => n,Playback(" + playbackPath(c.PromptFile) + ")\n")
		// 等待期间监听 DTMF：4 -> 视为用户无法接收传真
		fmt.Fprintf(&b, " same => n,WaitExten(%d)\n", c.PromptWait)
	}

	if !c.ECM {
		b.WriteString(" same => n,Set(FAXOPT(ecm)=no)\n")
	}
	b.WriteString(" same => n,SendFAX(${DB(sipvoice/faxfile)},d)\n")
	b.WriteString(" same => n,Set(DB(sipvoice/faxresult)=${FAXOPT(status)}|页=${FAXOPT(pages)}|速率=${FAXOPT(rate)}|错误=${FAXOPT(error)})\n")
	fmt.Fprintf(&b, " same => n,Wait(%d)\n", c.HangupDelay)
	b.WriteString(" same => n,Hangup()\n")
	b.WriteString("\n")

	// 等待阶段的按键处理
	b.WriteString("; 用户按键：4 表示没有传真机，直接结束\n")
	b.WriteString("exten => 4,1,Set(DB(sipvoice/faxresult)=USER_NO_FAX|用户按键拒绝接收)\n")
	b.WriteString(" same => n,Wait(1)\n")
	b.WriteString(" same => n,Hangup()\n")
	b.WriteString("exten => i,1,NoOp(忽略按键 ${EXTEN})\n")
	b.WriteString(" same => n,Goto(fax-send,reply,1)\n")

	if err := os.WriteFile(path, []byte(b.String()), 0o664); err != nil {
		return err
	}
	_ = exec.Command("chgrp", "sipvoice", path).Run()
	_ = os.Chmod(path, 0o664)

	ecm := "1"
	if !c.ECM {
		ecm = "0"
	}
	_, _ = astCLI("database put sipvoice fax_ecm " + ecm)
	_, _ = astCLI(fmt.Sprintf("database put sipvoice fax_ext %s", c.InboundExt))
	_, _ = astCLI(fmt.Sprintf("database put sipvoice fax_wait %d", c.HangupDelay))

	_, err := astCLI("dialplan reload")
	return err
}

// ensureInclude 确保 extensions.conf 里有对传真拨号文件的 include。
func ensureInclude() {
	const main = "/etc/asterisk/extensions.conf"
	const line = "#include extensions_sipfax.conf"
	raw, err := os.ReadFile(main)
	if err != nil {
		return
	}
	if strings.Contains(string(raw), line) {
		return
	}
	f, err := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o664)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n" + line + "\n")
}

// playbackPath 把文件路径转成 Playback 用的路径（去掉 .wav 后缀）。
func playbackPath(p string) string {
	return strings.TrimSuffix(p, ".wav")
}

// List 返回收到的传真（按时间倒序）。
func List(c Config) ([]Item, error) {
	entries, err := os.ReadDir(c.Spool)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".tiff") && !strings.HasSuffix(lower, ".tif") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := Item{
			Name:    name,
			Path:    filepath.Join(c.Spool, name),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
			Caller:  callerFromName(name),
		}
		item.Pages = countPages(item.Path)
		_, perr := os.Stat(previewPath(name))
		item.Preview = perr == nil
		item.Summary = LoadText(item.Path)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	return out, nil
}

func callerFromName(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".tiff"), ".tif")
	parts := strings.Split(base, "-")
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func countPages(path string) int {
	out, err := exec.Command("tiffinfo", path).CombinedOutput()
	if err != nil {
		return -1
	}
	return strings.Count(string(out), "TIFF directory")
}

func previewPath(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".tiff"), ".tif")
	return filepath.Join("/tmp", "sipvoice-fax-"+base+".png")
}

// Preview 把指定传真转成 PNG 预览，返回文件列表。
func Preview(itemName string, cfg Config) ([]string, error) {
	name := filepath.Base(itemName)
	src := filepath.Join(cfg.Spool, name)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("找不到文件: %s", name)
	}
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".tiff"), ".tif")
	prefix := filepath.Join("/tmp", "sipvoice-fax-"+base)
	matches, _ := filepath.Glob(prefix + "-*.png")
	for _, m := range matches {
		_ = os.Remove(m)
	}
	cmd := exec.Command("convert", src, "-resize", "1200x", prefix+"-%d.png")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("转换失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	files, _ := filepath.Glob(prefix + "-*.png")
	sort.Strings(files)
	return files, nil
}

// SafePreviewPath 校验预览路径合法（防目录穿越）。
func SafePreviewPath(name string) (string, error) {
	name = filepath.Base(filepath.Clean(name))
	if !strings.HasSuffix(name, ".png") || !strings.HasPrefix(name, "sipvoice-fax-") {
		return "", fmt.Errorf("非法预览名")
	}
	p := filepath.Join("/tmp", name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("预览不存在")
	}
	return p, nil
}

// SendResult 是一次发送的结束信息。
type SendResult struct {
	OK       bool   `json:"ok"`
	Busy     bool   `json:"busy,omitempty"` // true 表示"上一份还在发"，应排队重试
	Message  string `json:"message"`
	Elapsed  int    `json:"elapsed_ms"`
	Archived string `json:"archived,omitempty"` // 失败归档后的路径（有值说明源文件已被移走）
}

// Status 返回当前发送状态。
func Status() map[string]any {
	state.mu.Lock()
	defer state.mu.Unlock()
	m := map[string]any{
		"running": state.running,
		"target":  state.target,
		"file":    state.file,
		"ok":      state.ok,
		"message": state.message,
	}
	if !state.started.IsZero() {
		m["started"] = state.started.Format("15:04:05")
		m["elapsed_ms"] = time.Since(state.started).Milliseconds()
	}
	return m
}

// Send 把 spool 里的传真发到指定分机（阻塞直到结束，建议在 goroutine 里调）。
func Send(cfg Config, itemName, dest string) SendResult {
	start := time.Now()
	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		// Busy 标记：调用方可据此排队重试，而不是算作失败
		return SendResult{OK: false, Busy: true, Message: "上一份传真还在发送中"}
	}
	state.running = true
	state.target = dest
	state.file = itemName
	state.started = start
	state.ok = false
	state.message = "发送中"
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.running = false
		state.finished = time.Now()
		state.mu.Unlock()
	}()

	name := filepath.Base(itemName)
	src := filepath.Join(cfg.Spool, name)
	// 允许传绝对路径（重试时文件已在失败归档目录里）
	if filepath.IsAbs(itemName) {
		src = itemName
	}
	if _, err := os.Stat(src); err != nil {
		// spool 里没有，再去失败归档目录找一次——支持"重发发送失败的传真"
		failDir := cfg.FailDir
		if failDir == "" {
			failDir = DefaultConfig().FailDir
		}
		alt := filepath.Join(failDir, name)
		if _, err2 := os.Stat(alt); err2 == nil {
			src = alt
		} else {
			return finish(false, "找不到传真文件: "+src+"（也不在 "+failDir+"）", start)
		}
	}
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return finish(false, "目标号码为空", start)
	}

	_, _ = astCLI("database del sipvoice faxresult")
	_, _ = astCLI(fmt.Sprintf("database put sipvoice faxfile %s", src))
	_, _ = astCLI(fmt.Sprintf("database put sipvoice faxdest %s", dest))
	// 进入发送逻辑：Local/reply@fax-send 会执行 ReceiveFAX 的对应端 SendFAX，
	// 由 SendFAX 自己负责呼叫目标（不能先拨目标再执行，方向会反）
	if _, err := astCLI("channel originate Local/reply@fax-send extension s@dummy"); err != nil {
		return finish(false, "发起呼叫失败: "+err.Error(), start)
	}

	// originate 是异步的：先等通道出现，再等它消失
	appeared := false
	waitBy := time.Now().Add(15 * time.Second)
	for time.Now().Before(waitBy) {
		out, _ := astCLI("core show channels")
		if strings.Contains(out, "reply@fax-send") {
			appeared = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if appeared {
		deadline := time.Now().Add(180 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := astCLI("core show channels")
			if !strings.Contains(out, "reply@fax-send") {
				break
			}
			time.Sleep(time.Second)
		}
	}
	time.Sleep(2 * time.Second)

	msg := ReadFaxResult()
	if strings.Contains(msg, "USER_NO_FAX") {
		out := HandleNoFaxPath(cfg, src, dest, "用户按键拒绝接收")
		return finishArchived(out.Message, start, out.FailedDir)
	}
	if strings.Contains(msg, "SUCCESS") {
		return finish(true, "发送成功 "+msg, start)
	}
	if msg == "" {
		msg = "未获取到发送结果"
	}
	// 发送失败（呼叫没建立或握手失败）也归档并通知，避免反复重试
	if !appeared {
		out := HandleNoFaxPath(cfg, src, dest, "呼叫未能建立")
		return finishArchived(out.Message, start, out.FailedDir)
	}
	out := HandleNoFaxPath(cfg, src, dest, "传真握手失败: "+msg)
	return finishArchived(out.Message, start, out.FailedDir)
}

func finish(ok bool, msg string, start time.Time) SendResult {
	el := int(time.Since(start).Milliseconds())
	state.mu.Lock()
	state.ok = ok
	state.message = msg
	state.finished = time.Now()
	state.mu.Unlock()
	return SendResult{OK: ok, Message: msg, Elapsed: el}
}

// finishArchived 在失败归档后调用，额外带上归档路径。
func finishArchived(msg string, start time.Time, archived string) SendResult {
	r := finish(false, msg, start)
	r.Archived = archived
	return r
}

// ReadLastResult 读取 Asterisk 日志里最近一条传真结果。
func ReadLastResult() string {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0 && i > len(lines)-200; i-- {
		l := lines[i]
		if idx := strings.Index(l, "结果="); idx >= 0 {
			return strings.TrimSpace(l[idx+len("结果="):])
		}
	}
	return ""
}

// ReadFaxResult 优先读 AstDB 里最近一次传真结果。
func ReadFaxResult() string {
	out, err := astCLI("database get sipvoice faxresult")
	if err == nil {
		if idx := strings.Index(out, ":"); idx >= 0 {
			v := strings.TrimSpace(out[idx+1:])
			if v != "" && !strings.Contains(v, "not found") {
				return v
			}
		}
	}
	return ReadLastResult()
}

// PromptExists 提示音频是否已生成。
func PromptExists(c Config) bool {
	if c.PromptFile == "" {
		return false
	}
	fi, err := os.Stat(c.PromptFile)
	return err == nil && fi.Size() > 1000
}

// SavePromptWAV 把 TTS 返回的音频转成 Asterisk 能播的 8k/16bit 单声道 WAV。
func SavePromptWAV(c Config, audio []byte) (float64, error) {
	if len(audio) == 0 {
		return 0, fmt.Errorf("音频为空")
	}
	if err := os.MkdirAll(filepath.Dir(c.PromptFile), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp("", "fax-prompt-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(audio); err != nil {
		tmp.Close()
		return 0, err
	}
	tmp.Close()

	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", tmpName, "-ar", "8000", "-ac", "1", "-c:a", "pcm_s16le", c.PromptFile).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffmpeg 转换失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("chmod", "0644", c.PromptFile).Run()
	return 0, nil
}

// SendOutcome 是一次发送的最终结论。
type SendOutcome struct {
	OK        bool
	Reason    string
	Message   string
	File      string
	Dest      string
	FailedDir string
	MailSent  bool
}

// HandleNoFax 用户无法接收传真时的收尾：归档到失败目录，并按配置发邮件。
func HandleNoFax(c Config, file, dest, reason string) SendOutcome {
	out := SendOutcome{Reason: "user_no_fax", File: file, Dest: dest}
	src := filepath.Join(c.Spool, filepath.Base(file))
	if _, err := os.Stat(src); err != nil {
		out.Message = "用户无法接收传真，但原文件已不存在: " + file
		return out
	}
	dir := c.FailDir
	if dir == "" {
		dir = DefaultConfig().FailDir
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		out.Message = "创建失败目录出错: " + err.Error()
		return out
	}
	base := filepath.Base(file)
	stem := strings.TrimSuffix(strings.TrimSuffix(base, ".tiff"), ".tif")
	target := filepath.Join(dir, fmt.Sprintf("%s-to%s-%s.tiff", stem, dest, time.Now().Format("150405")))
	if err := os.Rename(src, target); err != nil {
		if err2 := copyFile(src, target); err2 != nil {
			out.Message = "归档失败: " + err.Error()
			return out
		}
		_ = os.Remove(src)
	}
	_ = exec.Command("chown", "sipvoice:sipvoice", target).Run()
	out.FailedDir = target
	out.Message = "传真未能送达，已归档到 " + target
	if reason != "" {
		out.Message += "（" + reason + "）"
	}
	_, _ = astCLI("database put sipvoice faxresult USER_NO_FAX|目标=" + dest + "|文件=" + filepath.Base(target))

	if c.NotifyEmail {
		if msg, sent := notifyByMail(c, dest, target, out.Message); sent {
			out.MailSent = true
			out.Message += "；" + msg
		} else if msg != "" {
			out.Message += "；" + msg
		}
	}
	out.OK = false
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}

// notifyByMail 用配置好的邮局把失败传真发出去（带 PDF 附件）。
// 返回给用户看的说明，以及是否成功。
func notifyByMail(c Config, dest, tiffPath, reason string) (string, bool) {
	mst := mailStore()
	st := mst.Load()
	if !st.Enabled || (!st.Primary.Ready() && !st.Backup.Ready()) {
		return "", false
	}
	to := extensionEmail(dest)
	if to == "" {
		to = strings.TrimSpace(c.MailTo)
	}
	if to == "" {
		to = st.NotifyTo
	}
	if to == "" {
		to = mail.DefaultNotifyTo
	}
	to = strings.ReplaceAll(to, "{number}", dest)
	if to == "" {
		return "", false
	}

	atts := []mail.Attach{}
	if st.ReplyPDF {
		if pdf, err := mail.ConvertTIFFToPDF(tiffPath); err == nil {
			name := strings.TrimSuffix(filepath.Base(tiffPath), ".tiff") + ".pdf"
			atts = append(atts, mail.Attach{Name: name, Data: pdf, MIME: "application/pdf"})
		}
	}
	if len(atts) == 0 {
		if raw, err := os.ReadFile(tiffPath); err == nil {
			atts = append(atts, mail.Attach{Name: filepath.Base(tiffPath), Data: raw, MIME: "image/tiff"})
		}
	}

	subject := "【SIPVoice】传真未能送达：" + dest
	body := "一份传真没有送达指定分机。\r\n\r\n" +
		"目标分机：" + dest + "\r\n" +
		"原因：" + reason + "\r\n" +
		"存档：" + tiffPath + "\r\n" +
		"时间：" + time.Now().Format("2006-01-02 15:04:05") + "\r\n\r\n" +
		"附件是这份传真的 PDF 版本（用任意阅读器都能打开）。\r\n"

	via, err := mail.SendWithFallback(st, []string{to}, subject, body, atts)
	if err != nil {
		return "邮件通知失败: " + err.Error(), false
	}
	return "已邮件通知 " + to + "（" + via + "）", true
}

// ListFailed 返回失败目录里的文件。
func ListFailed(c Config) []map[string]any {
	dir := c.FailDir
	if dir == "" {
		dir = DefaultConfig().FailDir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":  e.Name(),
			"size":  info.Size(),
			"mtime": info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["mtime"].(string) > out[j]["mtime"].(string)
	})
	return out
}

// AtoiDefault 简单包装。
func AtoiDefault(s string, d int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return d
	}
	return v
}

// mailStore 返回邮件配置存储。
func mailStore() mail.Store {
	return mail.Store{Path: "/etc/sipvoice/mail.json"}
}

// extensionEmail 从话机台账里取该分机的收件邮箱。
func extensionEmail(number string) string {
	raw, err := os.ReadFile("/etc/sipvoice/phones.json")
	if err != nil {
		return ""
	}
	var list []struct {
		Number string `json:"number"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return ""
	}
	for _, p := range list {
		if p.Number == number {
			return strings.TrimSpace(p.Email)
		}
	}
	return ""
}

// textPath 返回某份传真的分析结果存放路径（与 TIFF 同目录、同名的 .txt）。
// 支持绝对路径与「仅文件名」两种入参。
func textPath(item string) string {
	p := item
	if !filepath.IsAbs(p) {
		p = filepath.Join(Load().Spool, filepath.Base(p))
	}
	base := strings.TrimSuffix(strings.TrimSuffix(p, ".tiff"), ".tif")
	return base + ".txt"
}

// SaveText 保存传真分析结果。
func SaveText(item, text string) error {
	return os.WriteFile(textPath(item), []byte(text), 0o644)
}

// LoadText 读取传真分析结果（没有则返回空串）。
func LoadText(item string) string {
	raw, err := os.ReadFile(textPath(item))
	if err != nil {
		return ""
	}
	return string(raw)
}

// Analyzed 判断某份传真是否已有分析结果。
func Analyzed(item string) bool {
	fi, err := os.Stat(textPath(item))
	return err == nil && fi.Size() > 0
}

// HandleNoFaxPath 与 HandleNoFax 相同，但接受完整的源文件路径
// （重试时源文件可能已经在失败归档目录里）。
func HandleNoFaxPath(c Config, srcPath, dest, reason string) SendOutcome {
	out := SendOutcome{Reason: "user_no_fax", File: srcPath, Dest: dest}
	if _, err := os.Stat(srcPath); err != nil {
		out.Message = "传真未能送达，但原文件已不存在: " + srcPath
		return out
	}
	dir := c.FailDir
	if dir == "" {
		dir = DefaultConfig().FailDir
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		out.Message = "创建失败目录出错: " + err.Error()
		return out
	}
	base := filepath.Base(srcPath)
	stem := strings.TrimSuffix(strings.TrimSuffix(base, ".tiff"), ".tif")
	target := filepath.Join(dir, fmt.Sprintf("%s-to%s-%s.tiff", stem, dest, time.Now().Format("150405")))
	if err := os.Rename(srcPath, target); err != nil {
		if err2 := copyFile(srcPath, target); err2 != nil {
			out.Message = "归档失败: " + err.Error()
			return out
		}
		_ = os.Remove(srcPath)
	}
	_ = exec.Command("chown", "sipvoice:sipvoice", target).Run()
	out.FailedDir = target
	out.Message = "传真未能送达，已归档到 " + target
	if reason != "" {
		out.Message += "（" + reason + "）"
	}
	_, _ = astCLI("database put sipvoice faxresult USER_NO_FAX|目标=" + dest + "|文件=" + filepath.Base(target))
	if c.NotifyEmail {
		if msg, sent := notifyByMail(c, dest, target, out.Message); sent {
			out.MailSent = true
			out.Message += "；" + msg
		} else if msg != "" {
			out.Message += "；" + msg
		}
	}
	out.OK = false
	return out
}

// originateCmd 根据目标号码选择通道类型：
//   - 600 这类“拨号方案里的扩展号”走 Local/，直接进 [internal] 上下文
//   - 1001 这类已注册的分机走 PJSIP/
func originateCmd(dest string) string {
	if isExtensionEndpoint(dest) {
		return fmt.Sprintf("channel originate PJSIP/%s extension reply@fax-send", dest)
	}
	return fmt.Sprintf("channel originate Local/%s@internal extension reply@fax-send", dest)
}

// isExtensionEndpoint 判断目标是否是已注册的 PJSIP 分机（有 endpoint 定义）。
func isExtensionEndpoint(dest string) bool {
	out, err := astCLI("pjsip show endpoint " + dest)
	if err != nil {
		return false
	}
	return strings.Contains(out, "Endpoint:") && !strings.Contains(out, "Unable to find")
}
