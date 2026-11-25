package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"sipvoice/internal/config"
)

const storageApply = "/usr/local/sbin/sipvoice-storage-apply.sh"

// storageStats 是磁盘占用情况。
type storageStats struct {
	Dir       string `json:"dir"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	LogPath   string `json:"log_path"`
	LogBytes  int64  `json:"log_bytes"`
	DiskFree  int64  `json:"disk_free"`
	DiskTotal int64  `json:"disk_total"`
}

func dirStats(dir string) (files int, bytes int64) {
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		files++
		bytes += info.Size()
		return nil
	})
	return
}

// handleStorage 读取/保存存储策略，并执行清理或轮转。
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	if cfg == nil {
		cfg = config.Default()
	}
	st := cfg.Storage
	if st.RecordingDir == "" {
		st.RecordingDir = "/data/recordings"
	}
	if st.QuotaGB <= 0 {
		st.QuotaGB = 5
	}
	if st.LogMaxMB <= 0 {
		st.LogMaxMB = 50
	}
	if st.LogKeep <= 0 {
		st.LogKeep = 5
	}

	switch r.Method {
	case http.MethodGet:
		files, bytes := dirStats(st.RecordingDir)
		stat := storageStats{Dir: st.RecordingDir, Files: files, Bytes: bytes, LogPath: "/var/log/asterisk/messages.log"}
		if fi, err := os.Stat(stat.LogPath); err == nil {
			stat.LogBytes = fi.Size()
		}
		stat.DiskFree, stat.DiskTotal = diskFree(st.RecordingDir)
		writeJSON(w, map[string]any{"config": st, "stats": stat})
	case http.MethodPost:
		var in config.StorageConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		action := r.URL.Query().Get("action")
		if action == "" {
			// 保存设置
			if in.RecordingDir == "" {
				in.RecordingDir = st.RecordingDir
			}
			cfg.Storage = in
			if err := config.Set(cfg); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out, err := s.applyStorage(in)
			if err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": out + " " + err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": out})
			return
		}
		// 清理 / 轮转
		args := []string{}
		switch action {
		case "clean":
			args = append(args, "--clean")
		case "rotate":
			args = append(args, "--rotate")
		default:
			http.Error(w, "未知操作", 400)
			return
		}
		args = append(args, "--rec", boolArg(in.RecordingEnabled), "--dir", in.RecordingDir,
			"--quota", itoa(in.QuotaGB), "--logmax", itoa(in.LogMaxMB), "--logkeep", itoa(in.LogKeep))
		out, err := runSudo(storageApply, args...)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "message": out})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": out})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) applyStorage(st config.StorageConfig) (string, error) {
	return runSudo(storageApply,
		"--rec", boolArg(st.RecordingEnabled),
		"--dir", st.RecordingDir,
		"--quota", itoa(st.QuotaGB),
		"--logmax", itoa(st.LogMaxMB),
		"--logkeep", itoa(st.LogKeep))
}

func runSudo(path string, args ...string) (string, error) {
	cmd := exec.Command("sudo", append([]string{"-n", path}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
