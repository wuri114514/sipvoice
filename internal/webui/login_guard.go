package webui

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 登录防护默认值
const (
	loginMaxFails   = 5                // 窗口内最多失败次数
	loginWindow     = 10 * time.Minute // 统计窗口
	loginBlockDur   = 10 * time.Minute // 触发后的封禁时长
	loginCleanEvery = 5 * time.Minute  // 多久清理一次过期记录
)

// loginGuard 按来源 IP 限制登录失败次数。
// 连续失败达到上限后，该 IP 在封闭期内的所有登录请求都会被直接拒绝。
type loginGuard struct {
	mu       sync.Mutex
	entries  map[string]*guardEntry
	lastGC   time.Time
	maxFails int
	window   time.Duration
	blockFor time.Duration
}

type guardEntry struct {
	fails    int
	firstAt  time.Time
	blocked  time.Time
	lastSeen time.Time
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		entries:  make(map[string]*guardEntry),
		lastGC:   time.Now(),
		maxFails: loginMaxFails,
		window:   loginWindow,
		blockFor: loginBlockDur,
	}
}

// clientIP 取真实来源 IP，优先用反向代理头（只在直连对端是内网/本机时才信任 XFF）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isLocalPeer(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
		if rip := r.Header.Get("X-Real-Ip"); rip != "" {
			return strings.TrimSpace(rip)
		}
	}
	return host
}

func isLocalPeer(h string) bool {
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// blocked 判断该 IP 是否处于封禁期，并返回剩余时间。
func (g *loginGuard) blocked(ip string) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[ip]
	if e == nil || e.blocked.IsZero() {
		return false, 0
	}
	left := time.Until(e.blocked)
	if left <= 0 {
		// 封禁到期，清零重来
		e.blocked = time.Time{}
		e.fails = 0
		e.firstAt = time.Time{}
		return false, 0
	}
	return true, left
}

// fail 记录一次失败；返回是否因此触发封禁。
func (g *loginGuard) fail(ip string) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.collect(now)

	e := g.entries[ip]
	if e == nil {
		e = &guardEntry{}
		g.entries[ip] = e
	}
	e.lastSeen = now
	if e.firstAt.IsZero() || now.Sub(e.firstAt) > g.window {
		e.firstAt = now
		e.fails = 0
	}
	e.fails++
	if e.fails >= g.maxFails && e.blocked.IsZero() {
		e.blocked = now.Add(g.blockFor)
		return true, e.fails
	}
	return false, e.fails
}

// success 登录成功后清除该 IP 的失败记录。
func (g *loginGuard) success(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, ip)
}

// collect 清理过期条目，避免 map 无限增长。
func (g *loginGuard) collect(now time.Time) {
	if now.Sub(g.lastGC) < loginCleanEvery {
		return
	}
	g.lastGC = now
	for ip, e := range g.entries {
		if now.Sub(e.lastSeen) > g.window+g.blockFor {
			delete(g.entries, ip)
		}
	}
}

// status 返回当前被封锁的 IP 列表（供面板展示）。
func (g *loginGuard) status() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := make([]map[string]any, 0, len(g.entries))
	for ip, e := range g.entries {
		if e.blocked.IsZero() || now.After(e.blocked) {
			continue
		}
		out = append(out, map[string]any{
			"ip":        ip,
			"fails":     e.fails,
			"remaining": int(time.Until(e.blocked).Seconds()),
		})
	}
	return out
}

// unblock 手动解封。
func (g *loginGuard) unblock(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, ip)
}

// noteLoginFail 统一记录失败并打印日志。
func (s *Server) noteLoginFail(r *http.Request, name, reason string) {
	ip := clientIP(r)
	banned, fails := s.guard.fail(ip)
	if banned {
		log.Printf("[webui] 登录失败 %d 次，已封禁 IP %s（%s）：用户=%q 原因=%s",
			fails, ip, loginBlockDur, name, reason)
		return
	}
	log.Printf("[webui] 登录失败 IP=%s 用户=%q 原因=%s（第 %d/%d 次）",
		ip, name, reason, fails, loginMaxFails)
}
