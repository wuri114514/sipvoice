// vision.go —— 图片理解接口：把图片交给当前配置的对话模型（需支持视觉）
package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/config"
)

// uploadDir 是 SFTP「上传区」的真实路径。
const uploadDir = "/data/sipvoice-files/上传区"

// handleUploads 列出上传区里的图片。
func (s *Server) handleUploads(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "files": []string{}})
		return
	}
	var out []map[string]any
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		low := strings.ToLower(name)
		if !strings.HasSuffix(low, ".jpg") && !strings.HasSuffix(low, ".jpeg") &&
			!strings.HasSuffix(low, ".png") && !strings.HasSuffix(low, ".webp") {
			continue
		}
		info, err := e.Info()
		size := int64(0)
		mt := ""
		if err == nil {
			size = info.Size()
			mt = info.ModTime().Format("2006-01-02 15:04")
		}
		out = append(out, map[string]any{"name": name, "size": size, "mtime": mt})
	}
	writeJSON(w, map[string]any{"ok": true, "files": out, "dir": uploadDir})
}

// handleVision 把图片交给对话模型分析。
// 入参：multipart（file）或 JSON {name: "上传区里的文件名", prompt: "..."}
func (s *Server) handleVision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var (
		data    []byte
		name    string
		prompt  string
		maxSize = int64(20 << 20)
	)

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxSize); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "上传解析失败: " + err.Error()})
			return
		}
		prompt = strings.TrimSpace(r.FormValue("prompt"))
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "缺少图片文件"})
			return
		}
		defer f.Close()
		name = hdr.Filename
		data, err = io.ReadAll(io.LimitReader(f, maxSize))
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "读取失败: " + err.Error()})
			return
		}
	} else {
		var in struct {
			Name   string `json:"name"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "参数解析失败"})
			return
		}
		prompt = strings.TrimSpace(in.Prompt)
		name = filepath.Base(in.Name)
		p := filepath.Join(uploadDir, name)
		raw, err := os.ReadFile(p)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "读取上传区文件失败: " + err.Error()})
			return
		}
		data = raw
	}

	if len(data) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "图片为空"})
		return
	}
	if len(data) > 10<<20 {
		writeJSON(w, map[string]any{"ok": false,
			"error": fmt.Sprintf("图片 %.1f MB，超过多数视觉模型的 10MB 上限", float64(len(data))/(1<<20))})
		return
	}
	if prompt == "" {
		prompt = "用一句中文描述这张图片的主要内容。如果里面有文字，请把关键文字也说出来。"
	}

	cfg := config.Get()
	chat := aibuild.BuildChat(cfg)
	if chat == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "对话模型未配置"})
		return
	}
	// Vision 只在 OpenAI 兼容的 HTTP 客户端上提供（本地命令行模型不支持图片）
	vc, ok := chat.(interface {
		Vision(ctx context.Context, prompt, dataURL string) (string, error)
	})
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "当前对话后端不支持图片输入，请换用支持视觉的云端模型"})
		return
	}

	mime := "image/jpeg"
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		mime = "image/png"
	case ".webp":
		mime = "image/webp"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)

	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()

	start := time.Now()
	reply, err := vc.Vision(ctx, prompt, dataURL)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "ms": ms})
		return
	}
	writeJSON(w, map[string]any{
		"ok":    true,
		"model": cfg.AI.ChatModel,
		"name":  name,
		"bytes": len(data),
		"reply": reply,
		"ms":    ms,
	})
}
