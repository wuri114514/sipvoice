// Package session 管理“同一号码 = 同一会话”的异步对话上下文。
//
// 设计要点：
//   - 会话以主叫号码为键，通话断开后仍保留一段时间（可配置），
//     这样用户挂断后再打回来，AI 还记得之前聊了什么；
//   - 历史以标准 messages 数组保存（system / user / assistant），
//     直接送给 OpenAI 兼容接口，效果比把历史拼进 system prompt 好；
//   - 会话里可以挂“待办任务”（待发的传真、待回拨等），由后台 worker 执行。
package session

import (
	"sync"
	"time"
)

// Role 是消息角色。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message 是一条对话消息。
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
	// Kind 标记这条消息的来源："" 普通对话 / "fax" 传真相关 / "image" 图片
	Kind string `json:"kind,omitempty"`
}

// State 是会话状态。
type State string

const (
	StateIdle      State = "idle"       // 通话已结束，等待用户回拨
	StateInCall    State = "in_call"    // 通话中
	StatePendingTx State = "pending_tx" // 有待发的传真
	StateSending   State = "sending"    // 正在主动外呼（发传真或回拨）
)

// Task 是挂在会话上的待办任务。
type Task struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"` // "fax"（发传真）/ "callback"（回拨）
	CreatedAt time.Time      `json:"created_at"`
	Attempts  int            `json:"attempts"`
	Done      bool           `json:"done"`
	Failed    bool           `json:"failed"`
	Error     string         `json:"error,omitempty"`
	Params    map[string]any `json:"params,omitempty"` // fax: file/源；callback: 要说的话
}

// Session 是一个号码的对话上下文。
type Session struct {
	Key       string    `json:"key"`
	Messages  []Message `json:"messages"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Tasks     []Task    `json:"tasks,omitempty"`

	mu sync.Mutex
}

// TTL 是会话保留时长上限，防止配置异常导致内存无限增长。
const maxTTL = 24 * time.Hour

// Manager 管理所有会话。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
	maxMsgs  int
}

// New 创建管理器。
func New(ttl time.Duration, maxMsgs int) *Manager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	if maxMsgs <= 0 {
		maxMsgs = 20
	}
	m := &Manager{sessions: map[string]*Session{}, ttl: ttl, maxMsgs: maxMsgs}
	go m.gcLoop()
	return m
}

// SetTTL 热更新保留时长与历史条数上限。
func (m *Manager) SetTTL(ttl time.Duration, maxMsgs int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ttl > 0 {
		if ttl > maxTTL {
			ttl = maxTTL
		}
		m.ttl = ttl
	}
	if maxMsgs > 0 {
		m.maxMsgs = maxMsgs
	}
}

// Get 取会话（不存在返回 nil）。
func (m *Manager) Get(key string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok || time.Now().After(s.ExpiresAt) {
		if ok {
			delete(m.sessions, key)
		}
		return nil
	}
	return s
}

// Ensure 取或创建会话，并把状态置为通话中。
func (m *Manager) Ensure(key string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	s, ok := m.sessions[key]
	if !ok || now.After(s.ExpiresAt) {
		s = &Session{Key: key, CreatedAt: now}
		m.sessions[key] = s
	}
	s.State = StateInCall
	s.ExpiresAt = now.Add(m.ttl)
	s.UpdatedAt = now
	return s
}

// Touch 续期（通话中每次交互都会调用）。
func (m *Manager) Touch(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[key]; ok {
		now := time.Now()
		s.UpdatedAt = now
		s.ExpiresAt = now.Add(m.ttl)
	}
}

// EndCall 通话结束：状态回到空闲，但仍保留 TTL。
func (m *Manager) EndCall(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[key]; ok {
		s.State = StateIdle
		now := time.Now()
		s.UpdatedAt = now
		s.ExpiresAt = now.Add(m.ttl)
	}
}

// Append 追加一条消息（自动裁剪到上限）。
func (m *Manager) Append(key, role, content, kind string) {
	m.mu.Lock()
	s, ok := m.sessions[key]
	max := m.maxMsgs
	ttl := m.ttl
	m.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, Message{Role: role, Content: content, At: time.Now(), Kind: kind})
	if len(s.Messages) > max {
		s.Messages = s.Messages[len(s.Messages)-max:]
	}
	now := time.Now()
	s.UpdatedAt = now
	s.ExpiresAt = now.Add(ttl)
}

// History 返回可直接送给模型的 messages（不含 system）。
func (m *Manager) History(key string) []Message {
	s := m.Get(key)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// AddTask 给会话挂一个待办任务。
func (m *Manager) AddTask(key string, t Task) {
	m.mu.Lock()
	s, ok := m.sessions[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		t.ID = time.Now().Format("150405.000")
	}
	t.CreatedAt = time.Now()
	s.Tasks = append(s.Tasks, t)
	if t.Type == "fax" {
		s.State = StatePendingTx
	}
}

// PendingTasks 返回所有会话里未完成的任务（供后台 worker 轮询）。
func (m *Manager) PendingTasks() []struct {
	Key string
	Task
} {
	m.mu.Lock()
	list := make([]*Session, 0, len(m.sessions))
	now := time.Now()
	for k, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, k)
			continue
		}
		list = append(list, s)
	}
	m.mu.Unlock()

	var out []struct {
		Key string
		Task
	}
	for _, s := range list {
		s.mu.Lock()
		for _, t := range s.Tasks {
			if !t.Done && !t.Failed {
				out = append(out, struct {
					Key string
					Task
				}{Key: s.Key, Task: t})
			}
		}
		s.mu.Unlock()
	}
	return out
}

// FinishTask 标记任务结束。
func (m *Manager) FinishTask(key, id string, failed bool, errMsg string) {
	m.mu.Lock()
	s, ok := m.sessions[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Tasks {
		if s.Tasks[i].ID != id {
			continue
		}
		s.Tasks[i].Done = !failed
		s.Tasks[i].Failed = failed
		s.Tasks[i].Error = errMsg
		if failed {
			s.Tasks[i].Attempts++
		}
	}
	// 任务结束后状态复位；in_call 保留（那是真实通话中）
	if s.State == StatePendingTx || s.State == StateSending {
		s.State = StateIdle
	}
}

// List 返回所有未过期会话（面板展示用）。
func (m *Manager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []map[string]any
	for _, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			continue
		}
		s.mu.Lock()
		pending := 0
		for _, t := range s.Tasks {
			if !t.Done && !t.Failed {
				pending++
			}
		}
		out = append(out, map[string]any{
			"key":        s.Key,
			"state":      s.State,
			"messages":   len(s.Messages),
			"pending":    pending,
			"updated":    s.UpdatedAt.Format("15:04:05"),
			"expires_in": int(time.Until(s.ExpiresAt).Seconds()),
		})
		s.mu.Unlock()
	}
	return out
}

// Clear 清空某个会话。
func (m *Manager) Clear(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
}

// gcLoop 定期清理过期会话。
func (m *Manager) gcLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		now := time.Now()
		for k, s := range m.sessions {
			if now.After(s.ExpiresAt) {
				delete(m.sessions, k)
			}
		}
		m.mu.Unlock()
	}
}
