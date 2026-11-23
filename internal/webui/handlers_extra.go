package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"sipvoice/internal/config"
	"sipvoice/internal/sysinfo"
)

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"now":     sysinfo.Collect(),
		"history": sysinfo.History(),
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	active := config.Get().AI.ASRModelPath
	writeJSON(w, map[string]any{
		"dir":    s.opt.Models.Dir(),
		"models": s.opt.Models.List(active),
	})
}

func (s *Server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.opt.Models.Download(context.Background(), in.Name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.opt.Models.Delete(in.Name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleModelActivate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if strings.ContainsAny(in.Name, `/\`) || !strings.HasSuffix(in.Name, ".bin") {
		http.Error(w, "非法文件名", 400)
		return
	}
	cfg := *config.Get()
	cfg.AI.ASRModelPath = filepath.Join(s.opt.Models.Dir(), in.Name)
	cfg.AI.ASRBackend = "whisper-cli"
	if err := config.Set(&cfg); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.opt.OnConfig != nil {
		s.opt.OnConfig(&cfg)
	}
	writeJSON(w, map[string]any{"ok": true, "active": cfg.AI.ASRModelPath})
}

func (s *Server) handleModelUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 30); err != nil {
		http.Error(w, "上传失败: "+err.Error(), 400)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "缺少文件字段 file", 400)
		return
	}
	defer f.Close()
	name, err := s.opt.Models.Upload(io.LimitReader(f, 4<<30), hdr.Filename)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}
