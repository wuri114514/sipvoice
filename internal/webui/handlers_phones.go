package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"sipvoice/internal/fax"
	"sipvoice/internal/phones"
)

type phoneAction struct {
	Action string       `json:"action"`
	Phone  phones.Phone `json:"phone"`
	Number string       `json:"number"`
}

func (s *Server) handlePhones(w http.ResponseWriter, r *http.Request) {
	if s.opt.Phones == nil {
		http.Error(w, "话机管理未初始化", 500)
		return
	}
	switch r.Method {
	case http.MethodGet:
		contacts, err := phones.Contacts()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		writeJSON(w, map[string]any{
			"phones":   s.opt.Phones.List(),
			"contacts": contacts,
			"error":    msg,
		})
	case http.MethodPost:
		var in phoneAction
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var err error
		switch in.Action {
		case "upsert":
			err = s.opt.Phones.Upsert(in.Phone)
			if err == nil {
				err = s.applyPhoneConfig()
			}
		case "delete":
			err = s.opt.Phones.Delete(in.Number)
			if err == nil {
				err = s.applyPhoneConfig()
			}
		case "apply":
			err = s.applyPhoneConfig()
		default:
			http.Error(w, "未知操作", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) applyPhoneConfig() error {
	pj := s.opt.Phones.RenderPJSIP()
	ex := s.opt.Phones.RenderExtensions()
	if err := phones.Apply(map[string]string{
		s.opt.PJSIPConf:      pj,
		s.opt.ExtensionsConf: ex,
	}, s.opt.BackupDir); err != nil {
		return err
	}
	// 主文件里 include 了 extensions_sipfax.conf。话机模板会整份重写主文件，
	// 所以这里按当前传真配置把 [fax-send] / [fax-callback] 一起重建，
	// 保证「保存话机」既不会丢掉主叫号码映射，也不会把回拨上下文留成旧版本。
	if err := fax.ApplyDialplan(fax.Load()); err != nil {
		log.Printf("[webui] 保存话机后重建传真/回拨拨号方案失败: %v", err)
	}
	return nil
}

func (s *Server) handlePhoneTemplate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Phones == nil {
		http.Error(w, "话机管理未初始化", 500)
		return
	}
	number := r.URL.Query().Get("number")
	host := r.URL.Query().Get("host")
	port := r.URL.Query().Get("port")
	if host == "" {
		host = strings.Split(r.Host, ":")[0]
	}
	if port == "" {
		port = "5060"
	}
	txt, err := s.opt.Phones.Template(number, host, port)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(txt))
}
