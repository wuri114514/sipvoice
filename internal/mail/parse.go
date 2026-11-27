package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"net/textproto"
	"path/filepath"
	"strings"
)

// Incoming 是一封解析好的来信。
type Incoming struct {
	UID         int      `json:"uid"`
	From        string   `json:"from"`
	FromName    string   `json:"from_name"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"` // 纯文本正文（没有 text/plain 时用 HTML 转出来）
	Attachments []Attach `json:"attachments"`

	htmlBody string // 暂存 text/html，等所有分段走完再看要不要兜底
}

// 能当传真页面处理的附件类型。PDF 也收，ImageMagick 会逐页栅格化。
var faxPageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".bmp": true, ".tif": true, ".tiff": true, ".webp": true, ".pdf": true,
}

// ParseMessage 解析一封 RFC822 原文：主题、发件人、正文、图片/PDF 附件。
// 正文优先取 text/plain，没有才退回 text/html 去标签。
func ParseMessage(uid int, raw []byte, maxAttachMB int) (*Incoming, error) {
	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("解析邮件失败: %w", err)
	}
	dec := headerDecoder()
	in := &Incoming{UID: uid, Subject: decodeHeader(dec, msg.Header.Get("Subject"))}

	if addr, err := stdmail.ParseAddress(msg.Header.Get("From")); err == nil {
		in.From = strings.ToLower(strings.TrimSpace(addr.Address))
		in.FromName = decodeHeader(dec, addr.Name)
	} else {
		in.From = strings.ToLower(strings.TrimSpace(msg.Header.Get("From")))
	}

	maxBytes := int64(maxAttachMB) << 20
	if maxBytes <= 0 {
		maxBytes = int64(defaultMaxAttachMB) << 20
	}
	_ = walkPart(textproto.MIMEHeader(msg.Header), msg.Body, in, 0, maxBytes)
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" && in.htmlBody != "" {
		in.Body = htmlToText(in.htmlBody)
	}
	in.htmlBody = ""
	return in, nil
}

func walkPart(h textproto.MIMEHeader, r io.Reader, in *Incoming, depth int, maxBytes int64) error {
	if depth > 6 {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil
		}
		mr := multipart.NewReader(r, boundary)
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break // 分段损坏，后面的内容也不要了
			}
			_ = walkPart(p.Header, p, in, depth+1, maxBytes)
		}
		return nil
	}

	disp, dispParams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = decodeHeader(headerDecoder(), filename)
	isAttachment := strings.EqualFold(disp, "attachment") || filename != ""

	// 图片 / PDF：不管是内嵌还是附件，都当传真页面收集。
	//
	// 有些客户端（Foxmail / Outlook）把内嵌图片标成 application/octet-stream，
	// 文件名直接用 Content-ID（没有任何扩展名），单纯按 MIME 和扩展名判定会漏掉。
	// 所以对附件类分段先解码，再按文件头嗅探一次真实类型。
	if (isAttachment || strings.HasPrefix(mediaType, "image/") || isInlineCID(h)) &&
		!strings.HasPrefix(mediaType, "text/") {

		data, err := decodeTransfer(h.Get("Content-Transfer-Encoding"), r, maxBytes)
		if err == nil && int64(len(data)) <= maxBytes && len(data) > 0 {
			detected := ""
			if !isFaxPage(mediaType, filename) {
				detected = sniffMediaType(data)
			}
			if isFaxPage(mediaType, filename) || detected != "" {
				if detected != "" {
					mediaType = detected
				}
				// 文件名不像传真页时重新命名。
				// 注意不能用「扩展名为空」判断：Content-ID 形式的名字常带一个
				// 看起来像扩展名的尾巴（xxx@host.9748096B0），不能用它当扩展名。
				if !faxPageExts[strings.ToLower(filepath.Ext(filename))] {
					filename = fmt.Sprintf("image-%d%s", len(in.Attachments)+1, extFor(mediaType))
				}
				in.Attachments = append(in.Attachments, Attach{Name: filename, Data: data, MIME: mediaType})
				return nil
			}
		}
	}

	switch {
	case mediaType == "text/plain" && !isAttachment:
		if in.Body == "" {
			if b, err := decodeTransfer(h.Get("Content-Transfer-Encoding"), r, 1<<20); err == nil {
				in.Body = string(b)
			}
		}
	case mediaType == "text/html" && !isAttachment:
		// 先暂存，走完所有分段后如果压根没有 text/plain 再用它兜底
		if in.htmlBody == "" {
			if b, err := decodeTransfer(h.Get("Content-Transfer-Encoding"), r, 1<<20); err == nil {
				in.htmlBody = string(b)
			}
		}
	}
	return nil
}

func isFaxPage(mediaType, filename string) bool {
	if faxPageExts[strings.ToLower(filepath.Ext(filename))] {
		return true
	}
	return strings.HasPrefix(mediaType, "image/") || mediaType == "application/pdf"
}

// isInlineCID 判断分段是否是内嵌资源（带 Content-ID 或 inline 处置）。
// 邮件客户端把正文里的图片挂在 cid: 上时就是这种形态。
func isInlineCID(h textproto.MIMEHeader) bool {
	if strings.EqualFold(strings.TrimSpace(h.Get("Content-Disposition")), "inline") {
		return true
	}
	cid := strings.TrimSpace(h.Get("Content-ID"))
	return cid != "" && strings.Contains(cid, "@")
}

// sniffMediaType 按文件头识别常见图像与 PDF；识别不出返回空字符串。
//
// 需要它的原因是内嵌图片常常被标成 application/octet-stream 且没有扩展名，
// 只看 MIME 与文件名会把真正的传真页误判成普通附件丢掉。
func sniffMediaType(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 6 && (bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))):
		return "image/gif"
	case len(b) >= 2 && b[0] == 'B' && b[1] == 'M':
		return "image/bmp"
	case len(b) >= 4 && (bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*"))):
		return "image/tiff"
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 5 && bytes.HasPrefix(b, []byte("%PDF-")):
		return "application/pdf"
	}
	return ""
}

func extFor(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "application/pdf":
		return ".pdf"
	default:
		return ".jpg"
	}
}

// decodeTransfer 解 base64 / quoted-printable，并限制读取上限。
func decodeTransfer(enc string, r io.Reader, max int64) ([]byte, error) {
	var reader io.Reader = r
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		reader = quotedprintable.NewReader(r)
	}
	if max <= 0 {
		max = 1 << 20
	}
	return io.ReadAll(io.LimitReader(reader, max+1))
}

func headerDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		// 没有 x/text，GBK 之类的非 UTF-8 头按原始字节透传：
		// ASCII（包括 [FAX]）不受影响，只是中文显示可能是乱码。
		return input, nil
	}}
}

func decodeHeader(dec *mime.WordDecoder, s string) string {
	if s == "" {
		return ""
	}
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// htmlToText 去掉标签和常见实体，压成一行——够用来找 [号码]。
func htmlToText(h string) string {
	var b strings.Builder
	inTag := false
	for _, r := range h {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	s := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&apos;", "'",
	).Replace(b.String())
	return strings.Join(strings.Fields(s), " ")
}
