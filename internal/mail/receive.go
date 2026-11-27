package mail

import (
	"bytes"
	stdmail "net/mail"
	"regexp"
	"strings"
)

// ReceiveConfig 是「收信转传真（Email to Fax）」的设置。
//
// 触发规则（三项都要满足）：
//  1. 邮件在配置的信箱里处于未读状态；
//  2. 主题里带 SubjectTag（默认 "[FAX]"）；
//  3. 发件人命中 FromAllow（精确地址或 *@domain）。
//
// 目标号码从正文的方括号里取，例如「请发到 [2002] 谢谢」。
type ReceiveConfig struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`     // 如 imap.163.com
	Port        int      `json:"port"`     // 993（SSL）/ 143
	SSL         bool     `json:"ssl"`      // true=993 直连 TLS
	Username    string   `json:"username"` // 邮箱账号
	Password    string   `json:"password"` // 授权码
	Mailbox     string   `json:"mailbox"`  // 默认 INBOX（只支持 ASCII 名称）
	PollSeconds int      `json:"poll_seconds"`
	FromAllow   []string `json:"from_allow"` // 允许的发件人，空 = 谁都收（默认拒绝，必须配）
	SubjectTag  string   `json:"subject_tag"`
	MarkSeen    bool     `json:"mark_seen"`
	MaxAttachMB int      `json:"max_attach_mb"`
	// 下面两个字段由后台轮询写入，面板只读展示
	LastPoll   string `json:"last_poll,omitempty"`
	LastResult string `json:"last_result,omitempty"`
}

// Email to Fax 的默认值。
const (
	DefaultSubjectTag  = "[FAX]"
	DefaultMailbox     = "INBOX"
	defaultPollSeconds = 60
	defaultMaxAttachMB = 10
)

// DefaultReceiveConfig 返回一份可直接用的默认配置。
func DefaultReceiveConfig() ReceiveConfig {
	return ReceiveConfig{
		Port:        993,
		SSL:         true,
		Mailbox:     DefaultMailbox,
		PollSeconds: defaultPollSeconds,
		SubjectTag:  DefaultSubjectTag,
		MarkSeen:    true,
		MaxAttachMB: defaultMaxAttachMB,
	}
}

// Normalize 补齐默认值、清理空白。
func (r *ReceiveConfig) Normalize() {
	r.Host = strings.TrimSpace(r.Host)
	r.Username = strings.TrimSpace(r.Username)
	r.Mailbox = strings.TrimSpace(r.Mailbox)
	if r.Mailbox == "" {
		r.Mailbox = DefaultMailbox
	}
	if r.Port <= 0 {
		if r.SSL {
			r.Port = 993
		} else {
			r.Port = 143
		}
	}
	if r.PollSeconds < 15 {
		r.PollSeconds = defaultPollSeconds
	}
	if r.PollSeconds > 3600 {
		r.PollSeconds = 3600
	}
	if strings.TrimSpace(r.SubjectTag) == "" {
		r.SubjectTag = DefaultSubjectTag
	}
	if r.MaxAttachMB <= 0 {
		r.MaxAttachMB = defaultMaxAttachMB
	}
	if r.MaxAttachMB > 50 {
		r.MaxAttachMB = 50
	}

	seen := make(map[string]bool, len(r.FromAllow))
	out := make([]string, 0, len(r.FromAllow))
	for _, s := range r.FromAllow {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	r.FromAllow = out
}

// Ready 判断收信配置是否可用。
func (r ReceiveConfig) Ready() bool {
	return r.Host != "" && r.Username != "" && r.Password != ""
}

// SubjectIsFax 判断主题是否带触发标记（大小写不敏感，[fax] 也认）。
func (r ReceiveConfig) SubjectIsFax(subject string) bool {
	if r.SubjectTag == "" {
		return strings.Contains(strings.ToUpper(subject), DefaultSubjectTag)
	}
	return strings.Contains(strings.ToUpper(subject), strings.ToUpper(r.SubjectTag))
}

// SenderAllowed 判断发件人是否在白名单里。
// 支持精确地址（a@b.com）和域通配（*@b.com）；白名单为空时一律不放行。
func (r ReceiveConfig) SenderAllowed(from string) bool {
	from = strings.ToLower(strings.TrimSpace(from))
	if from == "" || len(r.FromAllow) == 0 {
		return false
	}
	// 自己发给自己也允许，方便用同一个邮箱做测试
	if r.Username != "" && from == strings.ToLower(strings.TrimSpace(r.Username)) {
		return true
	}
	at := strings.LastIndex(from, "@")
	domain := ""
	if at >= 0 {
		domain = from[at+1:]
	}
	for _, rule := range r.FromAllow {
		switch {
		case rule == from:
			return true
		case strings.HasPrefix(rule, "*@"):
			if domain != "" && domain == strings.TrimPrefix(rule, "*@") {
				return true
			}
		}
	}
	return false
}

// destRe 抓正文里的 [号码]，兼容全角括号和号码里的空格/短横线。
var destRe = regexp.MustCompile(`[(（]\s*(\+?\d[\d\s\-]{1,24})\s*[)）]`)

// ExtractDest 从正文里取第一个 [号码]；没有就返回空。
func ExtractDest(body string) string {
	// 正文里方括号是 ASCII [ ]，上面那条正则用圆括号是为了兼容中文全角，
	// 这里再单独扫一遍 ASCII 方括号，两种都支持。
	for _, m := range bracketRe.FindAllStringSubmatch(body, -1) {
		if d := cleanDest(m[1]); d != "" {
			return d
		}
	}
	for _, m := range destRe.FindAllStringSubmatch(body, -1) {
		if d := cleanDest(m[1]); d != "" {
			return d
		}
	}
	return ""
}

var bracketRe = regexp.MustCompile(`[\[［【]\s*(\+?\d[\d\s\-]{1,24})\s*[\]］】]`)

func cleanDest(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	if s == "" || s == "+" {
		return ""
	}
	return s
}

// FetchFaxCandidates 取「可能是传真请求」的未读邮件。
//
// 与 FetchUnseen 的区别：先只下载每封邮件的外部头部（From / Subject），
// 命中「主题带触发标记 + 发件人在白名单」的才下载完整正文。
//
// 这样做的原因是收件箱里往往堆着大量与传真无关的通知邮件：它们被跳过后
// 仍然保持未读，如果按「前 N 封未读」整封下载，真正的传真邮件很容易被挤到
// N 名之后而永远取不到。先看头部的成本极低，可以扫描更多未读邮件。
//
// 返回命中的邮件列表与本次扫描的未读总数。
func FetchFaxCandidates(cfg ReceiveConfig, limit int) (msgs []Incoming, scanned int, err error) {
	if limit <= 0 {
		limit = 100
	}
	err = withIMAP(cfg, func(c *imapClient) error {
		uids, err := c.searchUnseen()
		if err != nil {
			return err
		}
		scanned = len(uids)
		if len(uids) > limit {
			uids = uids[:limit]
		}
		for _, uid := range uids {
			hdr, err := c.fetchHeaders(uid)
			if err != nil {
				continue // 单封失败不影响其它邮件
			}
			subject, from := briefHeader(hdr)
			if !cfg.SubjectIsFax(subject) || !cfg.SenderAllowed(from) {
				continue // 不命中：保持未读，也不下载正文
			}
			raw, err := c.fetch(uid)
			if err != nil {
				continue
			}
			m, err := ParseMessage(uid, raw, cfg.MaxAttachMB)
			if err != nil {
				continue
			}
			msgs = append(msgs, *m)
		}
		return nil
	})
	return msgs, scanned, err
}

// briefHeader 从「只含 From / Subject 的头部块」里取出解码后的主题与发件人。
//
// 发件人必须取出纯地址：头部常见形态是 `"=?utf-8?B?...?=" <a@b.com>`，
// 直接拿原始字符串和白名单比对永远匹配不上。
func briefHeader(raw []byte) (subject, from string) {
	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}
	if addr, err := stdmail.ParseAddress(msg.Header.Get("From")); err == nil {
		from = strings.TrimSpace(addr.Address)
	} else {
		from = strings.TrimSpace(msg.Header.Get("From"))
	}
	return decodeHeader(headerDecoder(), msg.Header.Get("Subject")), from
}
