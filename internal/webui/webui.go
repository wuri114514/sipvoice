// Package webui 提供内嵌的配置与监控面板，以及与 agent 共用的通话记录存储。
package webui

import (
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"sipvoice/internal/aibuild"
	"sipvoice/internal/auth"
	"sipvoice/internal/config"
	"sipvoice/internal/mail"
	"sipvoice/internal/models"
	"sipvoice/internal/phones"
	"sipvoice/internal/session"
	"sipvoice/internal/sip"
	"sipvoice/internal/voice"
)

//go:embed index.html
var indexHTML []byte

// Turn 是一轮对话。
type Turn struct {
	At  time.Time `json:"at"`
	You string    `json:"you"`
	Bot string    `json:"bot"`
}

// Call 是一通电话的摘要。
type Call struct {
	UUID   string    `json:"uuid"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	DurMS  int64     `json:"dur_ms"`
	In     int64     `json:"in_frames"`
	Out    int64     `json:"out_frames"`
	Active bool      `json:"active"`
	Turns  []Turn    `json:"turns"`
}

// Store 保存最近的通话记录，实现 voice.Observer。
type Store struct {
	mu      sync.Mutex
	active  map[string]int
	calls   []*Call
	maxKeep int
	total   int
}

// NewStore 创建记录存储。
func NewStore() *Store {
	return &Store{active: map[string]int{}, maxKeep: 50}
}

// CallStart 实现 voice.Observer。
func (s *Store) CallStart(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[uuid]++
	s.total++
	c := &Call{UUID: uuid, Start: time.Now(), Active: true}
	s.calls = append([]*Call{c}, s.calls...)
	if len(s.calls) > s.maxKeep {
		s.calls = s.calls[:s.maxKeep]
	}
}

// CallEnd 实现 voice.Observer。
func (s *Store) CallEnd(uuid string, dur time.Duration, in, out int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[uuid] > 0 {
		s.active[uuid]--
		if s.active[uuid] == 0 {
			delete(s.active, uuid)
		}
	}
	for _, c := range s.calls {
		if c.UUID == uuid && c.Active {
			c.Active = false
			c.End = time.Now()
			c.DurMS = dur.Milliseconds()
			c.In = in
			c.Out = out
			return
		}
	}
}

// Turn 实现 voice.Observer。
func (s *Store) Turn(uuid, you, bot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.UUID == uuid {
			c.Turns = append(c.Turns, Turn{At: time.Now(), You: you, Bot: bot})
			return
		}
	}
}

// Active 返回当前通话数。
func (s *Store) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.active {
		n += v
	}
	return n
}

// Calls 返回最近通话的副本。
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, *c)
	}
	return out
}

// Options 是 WebUI 的启动参数。
type Options struct {
	Addr           string
	Version        string
	Agent          *voice.Agent
	Store          *Store
	Models         *models.Manager
	Auth           *auth.Store
	TL             *TLManager
	Phones         *phones.Store
	PJSIPConf      string
	ExtensionsConf string
	BackupDir      string
	OnConfig       func(*config.Config) // 保存后回调，用于热更新 provider
	Sessions       *session.Manager     // 异步会话（按主叫号码保留上下文）
	MailPath       string               // 邮箱设置文件，默认 /etc/sipvoice/mail.json
}

// Server 是 WebUI 服务器。
type Server struct {
	opt     Options
	started time.Time
	mu      sync.Mutex
	lastAST time.Time
	astUp   bool

	guard *loginGuard

	handler http.Handler
	srv     *http.Server
	srvMu   sync.Mutex
}

// Start 启动 WebUI（非阻塞）。
func Start(opt Options) *Server {
	s := &Server{opt: opt, started: time.Now(), guard: newLoginGuard()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/test", s.handleTest)
	mux.HandleFunc("/api/calls", s.handleCalls)
	mux.HandleFunc("/api/auth", s.handleAuthState)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/providers", s.handleProviders)
	mux.HandleFunc("/api/models/remote", s.handleRemoteModels)
	mux.HandleFunc("/api/test/tts", s.handleTestTTS)
	mux.HandleFunc("/api/test/asr", s.handleTestASR)
	mux.HandleFunc("/api/test/omni", s.handleTestOmni)
	mux.HandleFunc("/api/test/notify", s.handleTestNotify)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/download", s.handleModelDownload)
	mux.HandleFunc("/api/models/delete", s.handleModelDelete)
	mux.HandleFunc("/api/models/activate", s.handleModelActivate)
	mux.HandleFunc("/api/models/upload", s.handleModelUpload)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/tls", s.handleTLS)
	mux.HandleFunc("/api/tls/upload", s.handleTLSUpload)
	mux.HandleFunc("/api/phones", s.handlePhones)
	mux.HandleFunc("/api/phones/template", s.handlePhoneTemplate)
	mux.HandleFunc("/api/storage", s.handleStorage)
	mux.HandleFunc("/api/security", s.handleSecurity)
	mux.HandleFunc("/api/fax", s.handleFax)
	mux.HandleFunc("/api/mail", s.handleMail)
	mux.HandleFunc("/api/uploads", s.handleUploads)
	mux.HandleFunc("/api/vision", s.handleVision)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/session/task", s.handleSessionTask)
	mux.HandleFunc("/api/fax/analyze", s.handleFaxAnalyze)
	mux.HandleFunc("/api/voice/callback-intro", s.handleCallbackIntro)
	mux.HandleFunc("/api/config/export", s.handleConfigBundle)
	mux.HandleFunc("/api/config/import", s.handleConfigBundle)
	s.handler = s.withAuth(mux)
	go s.listen()
	return s
}

func (s *Server) listen() {
	mode := TLSOff
	if s.opt.TL != nil {
		mode = s.opt.TL.Mode()
	}
	ln, err := net.Listen("tcp", s.opt.Addr)
	if err != nil {
		log.Printf("[webui] 启动失败: %v", err)
		return
	}
	srv := &http.Server{Handler: s.handler}
	s.srvMu.Lock()
	s.srv = srv
	s.srvMu.Unlock()

	if mode == TLSOff || s.opt.TL == nil {
		log.Printf("[webui] 面板监听 http://%s", s.opt.Addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[webui] 服务退出: %v", err)
		}
		return
	}
	cert, err := s.opt.TL.Certificate()
	if err != nil {
		log.Printf("[webui] 证书加载失败，改用 HTTP: %v", err)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[webui] 服务退出: %v", err)
		}
		return
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	log.Printf("[webui] 面板监听 https://%s", s.opt.Addr)
	if err := srv.Serve(tls.NewListener(ln, cfg)); err != nil && err != http.ErrServerClosed {
		log.Printf("[webui] 服务退出: %v", err)
	}
}

// Restart 关闭旧监听并按当前 TLS 配置重新监听。
func (s *Server) Restart() {
	s.srvMu.Lock()
	srv := s.srv
	s.srvMu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	go s.listen()
}

// mailStore 返回邮箱设置存储；测试里可以把路径指到临时目录。
func (s *Server) mailStore() *mail.Store {
	p := s.opt.MailPath
	if p == "" {
		p = "/etc/sipvoice/mail.json"
	}
	return &mail.Store{Path: p}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// 面板 HTML 是编译进二进制的：升级后浏览器必须拿新的，
	// 否则旧页面会继续用旧的 JS（少字段、少按钮）去调新后端。
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// Status 是 /api/status 的返回。
type Status struct {
	Version     string `json:"version"`
	UptimeSec   int64  `json:"uptime_sec"`
	AudioListen string `json:"audio_listen"`
	AIBase      string `json:"ai_base"`
	Asterisk    bool   `json:"asterisk"`
	ActiveCalls int    `json:"active_calls"`
	TotalCalls  int    `json:"total_calls"`
	WebAddr     string `json:"web_addr"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	st := Status{
		Version:     s.opt.Version,
		UptimeSec:   int64(time.Since(s.started).Seconds()),
		AudioListen: cfg.Audio.Listen,
		AIBase:      cfg.AI.BaseURL,
		ActiveCalls: s.opt.Store.Active(),
		TotalCalls:  len(s.opt.Store.Calls()),
		WebAddr:     s.opt.Addr,
	}
	st.Asterisk = s.asteriskUp()
	writeJSON(w, st)
}

func (s *Server) asteriskUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastAST) < 5*time.Second {
		return s.astUp
	}
	s.lastAST = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, err := sip.Client(ctx, "127.0.0.1:5060")
	s.astUp = err == nil
	return s.astUp
}

// ConfigView 是脱敏后的配置。
type ConfigView struct {
	config.Config
	APIKeySet bool `json:"api_key_set"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[webui] 保存配置 panic: %v", rec)
			http.Error(w, "内部错误", 500)
		}
	}()
	switch r.Method {
	case http.MethodGet:
		cfg := config.Get()
		view := ConfigView{Config: *cfg, APIKeySet: cfg.AI.APIKey != ""}
		// 密钥单独加密存放（secrets.json），这里只回传掩码
		view.AI.APIKey = maskKey(cfg.AI.APIKey)
		view.AI.ASRKey = maskKey(cfg.AI.ASRKey)
		view.AI.TTSKey = maskKey(cfg.AI.TTSKey)
		view.Realtime.APIKey = maskKey(cfg.Realtime.APIKey)
		view.Notify.ServerChanKey = maskKey(cfg.Notify.ServerChanKey)
		// 各服务商的 key 只告诉前端"有没有存过"，不回传明文
		if len(cfg.AI.ProviderKeys) > 0 {
			view.AI.ProviderKeys = make(map[string]string, len(cfg.AI.ProviderKeys))
			for k, v := range cfg.AI.ProviderKeys {
				if v != "" {
					view.AI.ProviderKeys[k] = "***"
				}
			}
		}
		writeJSON(w, view)
	case http.MethodPost:
		var in config.Config
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "配置解析失败: "+err.Error(), 400)
			return
		}
		old := config.Get()
		if in.AI.APIKey == "***" || in.AI.APIKey == "" {
			in.AI.APIKey = old.AI.APIKey
		}
		if in.AI.ASRKey == "***" || in.AI.ASRKey == "" {
			in.AI.ASRKey = old.AI.ASRKey
		}
		if in.AI.TTSKey == "***" || in.AI.TTSKey == "" {
			in.AI.TTSKey = old.AI.TTSKey
		}
		if in.Realtime.APIKey == "***" || in.Realtime.APIKey == "" {
			in.Realtime.APIKey = old.Realtime.APIKey
		}
		if in.Notify.ServerChanKey == "***" || in.Notify.ServerChanKey == "" {
			in.Notify.ServerChanKey = old.Notify.ServerChanKey
		}
		if in.Audio.Listen == "" {
			in.Audio.Listen = old.Audio.Listen
		}
		// 合并各服务商的 key：前端传 *** 表示"保持原值"
		if old.AI.ProviderKeys != nil {
			if in.AI.ProviderKeys == nil {
				in.AI.ProviderKeys = map[string]string{}
			}
			for k, v := range old.AI.ProviderKeys {
				if cur, ok := in.AI.ProviderKeys[k]; !ok || cur == "***" || cur == "" {
					in.AI.ProviderKeys[k] = v
				}
			}
		}
		// 当前服务商填了新 key 时同步记录到 provider_keys
		if pid := in.AI.Provider; pid != "" && in.AI.APIKey != "" && in.AI.APIKey != "***" {
			if in.AI.ProviderKeys == nil {
				in.AI.ProviderKeys = map[string]string{}
			}
			in.AI.ProviderKeys[pid] = in.AI.APIKey
		}
		if err := config.Set(&in); err != nil {
			log.Printf("[webui] 保存配置失败: %v", err)
			http.Error(w, "保存失败: "+err.Error(), 500)
			return
		}
		if s.opt.OnConfig != nil {
			s.opt.OnConfig(&in)
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	c := aibuild.BuildChat(cfg)
	if c == nil {
		writeJSON(w, map[string]any{"ok": false, "message": "未配置 AI base_url"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	reply, err := c.Chat(ctx, "只回答两个字：收到", "ping")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": strings.TrimSpace(reply),
		"ms":      time.Since(start).Milliseconds(),
		"model":   cfg.AI.ChatModel,
	})
}

func (s *Server) handleCalls(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.opt.Store.Calls())
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	return "***"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
