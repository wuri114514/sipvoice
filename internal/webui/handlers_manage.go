package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	current, ok := s.loggedIn(r)
	if !ok {
		http.Error(w, "未登录", 401)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"users": s.opt.Auth.List(), "current": current})
	case http.MethodPost:
		var in struct {
			Action   string `json:"action"`
			Name     string `json:"name"`
			NewName  string `json:"new_name"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var err error
		switch in.Action {
		case "add":
			err = s.opt.Auth.Add(strings.TrimSpace(in.Name), in.Password)
		case "delete":
			if in.Name == current {
				http.Error(w, "不能删除当前登录的账号", 400)
				return
			}
			err = s.opt.Auth.Delete(in.Name)
		case "rename":
			err = s.opt.Auth.Rename(current, strings.TrimSpace(in.NewName))
			if err == nil {
				s.opt.Auth.SetSessionUser(s.sessionToken(r), strings.TrimSpace(in.NewName))
			}
		case "password":
			err = s.opt.Auth.SetPassword(current, in.Password)
		default:
			http.Error(w, "未知操作", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleTLS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		info := map[string]any{"addr": s.opt.Addr}
		if s.opt.TL != nil {
			for k, v := range s.opt.TL.Info() {
				info[k] = v
			}
		}
		writeJSON(w, info)
	case http.MethodPost:
		var in struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if s.opt.TL == nil {
			http.Error(w, "TLS 未初始化", 500)
			return
		}
		if err := s.opt.TL.SetMode(TLSMode(in.Mode)); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		// 等响应发完再重启监听，否则客户端收不到结果
		time.AfterFunc(600*time.Millisecond, s.Restart)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleTLSUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	certPEM, err := readFormBytes(r, "cert")
	if err != nil {
		http.Error(w, "缺少证书文件 cert", 400)
		return
	}
	keyPEM, err := readFormBytes(r, "key")
	if err != nil {
		http.Error(w, "缺少私钥文件 key", 400)
		return
	}
	if s.opt.TL == nil {
		http.Error(w, "TLS 未初始化", 500)
		return
	}
	if err := s.opt.TL.SetManual(certPEM, keyPEM); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	go s.Restart()
	writeJSON(w, map[string]any{"ok": true})
}

func readFormBytes(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 8<<20))
}
