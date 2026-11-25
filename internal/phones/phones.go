// Package phones 管理话机台账，并生成 Asterisk 的 pjsip/extensions 配置。
package phones

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Phone 是一台话机（分机）。
type Phone struct {
	Number         string `json:"number"`
	Name           string `json:"name"`
	Password       string `json:"password"`
	Enabled        bool   `json:"enabled"`
	AllowIntercall bool   `json:"allow_intercall"`
	Hotline        string `json:"hotline"`
	Note           string `json:"note"`
}

// Store 管理话机台账文件。
type Store struct {
	path string
}

// New 加载台账；文件不存在时用默认两台测试分机初始化。
func New(path string) *Store {
	s := &Store{path: path}
	if _, err := os.Stat(path); err != nil {
		_ = s.save([]Phone{
			{Number: "1001", Name: "测试话机 A", Password: "SipVoice1001", Enabled: true, AllowIntercall: true},
			{Number: "1002", Name: "测试话机 B", Password: "SipVoice1002", Enabled: true, AllowIntercall: true},
		})
	}
	return s
}

// List 返回全部话机（按号码排序）。
func (s *Store) List() []Phone {
	phones := s.load()
	sort.Slice(phones, func(i, j int) bool { return phones[i].Number < phones[j].Number })
	return phones
}

// Upsert 新增或更新一台话机。
func (s *Store) Upsert(p Phone) error {
	if p.Number == "" || p.Name == "" {
		return fmt.Errorf("号码和名称不能为空")
	}
	if p.Password == "" {
		p.Password = "Sip" + p.Number + "Pass"
	}
	phones := s.load()
	found := false
	for i := range phones {
		if phones[i].Number == p.Number {
			phones[i] = p
			found = true
			break
		}
	}
	if !found {
		phones = append(phones, p)
	}
	return s.save(phones)
}

// Delete 删除一台话机。
func (s *Store) Delete(number string) error {
	phones := s.load()
	out := phones[:0]
	for _, p := range phones {
		if p.Number != number {
			out = append(out, p)
		}
	}
	return s.save(out)
}

// Template 生成话机配置模板（供网关/软电话导入）。
func (s *Store) Template(number, host, port string) (string, error) {
	var p *Phone
	for i, x := range s.load() {
		if x.Number == number {
			pp := s.load()[i]
			p = &pp
			break
		}
	}
	if p == nil {
		return "", fmt.Errorf("分机不存在")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "分机号: %s\n显示名: %s\n", p.Number, p.Name)
	fmt.Fprintf(&b, "SIP 服务器: %s\n端口: %s\n传输: UDP\n", host, port)
	fmt.Fprintf(&b, "用户名: %s\n认证ID: %s\n密码: %s\n", p.Number, p.Number, p.Password)
	fmt.Fprintf(&b, "编解码: PCMU/8000, PCMA/8000\n")
	fmt.Fprintf(&b, "注册有效期: 300 秒\n")
	if p.Hotline != "" {
		fmt.Fprintf(&b, "摘机直通: %s（在网关的 Offhook Auto Dial 里填）\n", p.Hotline)
	}
	return b.String(), nil
}

func (s *Store) load() []Phone {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var phones []Phone
	_ = json.Unmarshal(raw, &phones)
	return phones
}

func (s *Store) save(phones []Phone) error {
	raw, _ := json.MarshalIndent(phones, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o660); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RenderPJSIP 生成 pjsip.conf 内容。
func (s *Store) RenderPJSIP() string {
	var b strings.Builder
	b.WriteString("; 由 SIPVoice 控制台生成，请勿手工修改\n\n")
	b.WriteString("; transport（含公网地址）放在 pjsip.d/transport.conf，控制台不会覆盖\n")
	b.WriteString("#include pjsip.d/transport.conf\n\n")

	for _, p := range s.List() {
		if !p.Enabled {
			continue
		}
		fmt.Fprintf(&b, "[%s]\ntype=endpoint\ncontext=ctx-%s\ndisallow=all\nallow=ulaw\nallow=alaw\n", p.Number, p.Number)
		fmt.Fprintf(&b, "auth=%s-auth\naors=%s\ndirect_media=no\nforce_rport=yes\nrewrite_contact=yes\n", p.Number, p.Number)
		b.WriteString("rtp_symmetric=yes\ndtmf_mode=rfc4733\ntransport=transport-udp\n\n")
		fmt.Fprintf(&b, "[%s-auth]\ntype=auth\nauth_type=userpass\nusername=%s\npassword=%s\n\n", p.Number, p.Number, p.Password)
		fmt.Fprintf(&b, "[%s]\ntype=aor\nmax_contacts=3\nremove_existing=yes\nqualify_frequency=30\n\n", p.Number)
	}

	b.WriteString("[calltest]\ntype=endpoint\ncontext=internal\ndisallow=all\nallow=ulaw\ndirect_media=no\n")
	b.WriteString("force_rport=yes\nrewrite_contact=yes\nrtp_symmetric=yes\ndtmf_mode=rfc4733\ntransport=transport-udp\n\n")
	b.WriteString("[calltest-identify]\ntype=identify\nendpoint=calltest\nmatch=127.0.0.1\n")
	return b.String()
}

// RenderExtensions 生成 extensions.conf 内容（含每台话机的互播策略）。
func (s *Store) RenderExtensions() string {
	all := s.List()
	var b strings.Builder
	b.WriteString("; 由 SIPVoice 控制台生成，请勿手工修改\n\n")
	b.WriteString("[general]\nstatic=yes\nwriteprotect=no\nclearglobalvars=no\n\n")
	b.WriteString("[globals]\n\n")

	b.WriteString("[internal]\n")
	b.WriteString("exten => 9000,1,NoOp(SIPVoice AI bot, caller=${CALLERID(num)})\n")
	b.WriteString(" same => n,Answer()\n")
	b.WriteString(" same => n,Set(RECDIR=${DB(sipvoice/rec_dir)})\n")
	b.WriteString(" same => n,GotoIf($[\"${DB(sipvoice/rec)}\" = \"1\" & \"${RECDIR}\" != \"\"]?rec:ai)\n")
	b.WriteString(" same => n(rec),MixMonitor(${RECDIR}/${STRFTIME(${EPOCH},,%Y%m%d-%H%M%S)}-${CALLERID(num)}.wav)\n")
	b.WriteString(" same => n(ai),AudioSocket(${SHELL(uuidgen | tr -d '\\n')},127.0.0.1:9092)\n")
	b.WriteString(" same => n,Hangup()\n")
	b.WriteString("exten => _+X.,1,Set(NUM=${EXTEN:1})\n same => n,Goto(internal,${NUM},1)\n")
	b.WriteString("exten => 600,1,NoOp(fax receive)\n same => n,Answer()\n same => n,Wait(1)\n same => n,ReceiveFAX(/var/spool/asterisk/fax/${STRFTIME(${EPOCH},,%Y%m%d-%H%M%S)}.tiff)\n same => n,Hangup()\n")
	b.WriteString("exten => _X.,1,Congestion()\n\n")

	for _, p := range all {
		if !p.Enabled {
			continue
		}
		fmt.Fprintf(&b, "[ctx-%s]\n", p.Number)
		b.WriteString("exten => 9000,1,Goto(internal,9000,1)\n")
		b.WriteString("exten => 600,1,Goto(internal,600,1)\n")
		if p.AllowIntercall {
			for _, q := range all {
				if !q.Enabled || q.Number == p.Number {
					continue
				}
				fmt.Fprintf(&b, "exten => %s,1,Dial(PJSIP/%s,30)\n same => n,Hangup()\n", q.Number, q.Number)
			}
		}
		b.WriteString("exten => _X.,1,Congestion()\n\n")
	}
	return b.String()
}

// Apply 写入配置并让 Asterisk 重新加载；备份写到 backupDir。
func Apply(content map[string]string, backupDir string) error {
	stamp := time.Now().Format("20060102-150405")
	for path, data := range content {
		if b, err := os.ReadFile(path); err == nil && backupDir != "" {
			_ = os.MkdirAll(backupDir, 0o750)
			name := filepath.Base(path) + ".bak." + stamp
			_ = os.WriteFile(filepath.Join(backupDir, name), b, 0o640)
		}
		if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", path, err)
		}
	}
	return Reload()
}

// astCLI 执行一条 Asterisk CLI 命令。
// 优先用 sudo（systemd 部署时服务用户需要提权）；容器里通常直接是 root，
// 没有 sudo 或不需要提权时自动退回直接调用。
func astCLI(cmd string) ([]byte, error) {
	out, err := exec.Command("sudo", "-n", "/usr/sbin/asterisk", "-rx", cmd).CombinedOutput()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return exec.Command("/usr/sbin/asterisk", "-rx", cmd).CombinedOutput()
	}
	// sudo 存在但被拒绝（非 root 且没有 sudoers 规则）时也尝试直连
	if out2, err2 := exec.Command("/usr/sbin/asterisk", "-rx", cmd).CombinedOutput(); err2 == nil {
		return out2, nil
	}
	return out, err
}

// Reload 让 Asterisk 重新加载配置。
func Reload() error {
	for _, cmd := range []string{"pjsip reload", "dialplan reload"} {
		out, err := astCLI(cmd)
		if err != nil {
			return fmt.Errorf("%s: %v: %s", cmd, err, string(out))
		}
	}
	return nil
}

// Contacts 返回已注册话机（解析 `pjsip show contacts`）。
func Contacts() ([]map[string]string, error) {
	out, err := astCLI("pjsip show contacts")
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, string(out))
	}
	var res []map[string]string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Contact:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "Contact:"))
		fields := strings.Fields(rest)
		if len(fields) < 3 || strings.Contains(fields[0], "<") {
			continue
		}
		aor := fields[0]
		if i := strings.Index(aor, "/"); i > 0 {
			aor = aor[:i]
		}
		item := map[string]string{"aor": aor, "uri": fields[0]}
		if len(fields) >= 3 {
			item["status"] = fields[len(fields)-2]
		}
		res = append(res, item)
	}
	return res, nil
}
