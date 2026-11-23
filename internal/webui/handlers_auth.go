package webui

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "sipvoice_session"

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.loggedIn(r)
	writeJSON(w, map[string]any{
		"need_setup": s.opt.Auth.NeedSetup(),
		"logged_in":  ok,
		"user":       user,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.opt.Auth.NeedSetup() {
		http.Error(w, "已初始化", 400)
		return
	}
	var in credentials
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := strings.TrimSpace(in.Username)
	if err := s.opt.Auth.Setup(name, in.Password); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.setCookie(w, s.opt.Auth.NewSession(name))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in credentials
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := strings.TrimSpace(in.Username)
	if !s.opt.Auth.Verify(name, in.Password) {
		time.Sleep(300 * time.Millisecond)
		http.Error(w, "用户名或密码错误", 401)
		return
	}
	s.setCookie(w, s.opt.Auth.NewSession(name))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.opt.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) loggedIn(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return s.opt.Auth.Valid(c.Value)
}

func (s *Server) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) isTLS() bool {
	return s.opt.TL != nil && s.opt.TL.Mode() != TLSOff
}

func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.isTLS(),
		MaxAge:   86400,
	})
}

// withAuth 包装 mux：除认证接口外，/api/ 一律要求登录。
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/api/auth" || p == "/api/login" || p == "/api/setup" || p == "/api/logout" || !strings.HasPrefix(p, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.loggedIn(r); !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"未登录"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
