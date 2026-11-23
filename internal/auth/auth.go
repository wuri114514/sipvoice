// Package auth 提供控制台的口令认证：用户文件存储、PBKDF2 口令校验、内存会话。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// User 是一个控制台账号。
type User struct {
	Name string `json:"name"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	Iter int    `json:"iter"`
}

// fileData 是 users.json 的结构。
type fileData struct {
	Users []User `json:"users"`
}

// Store 管理账号与会话。
type session struct {
	user string
	exp  time.Time
}

type Store struct {
	mu       sync.Mutex
	path     string
	users    []User
	sessions map[string]session
}

// New 加载（或初始化）账号文件。
func New(path string) (*Store, error) {
	s := &Store{path: path, sessions: map[string]session{}}
	raw, err := os.ReadFile(path)
	if err == nil {
		var d fileData
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("auth: 解析 %s: %w", path, err)
		}
		s.users = d.Users
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// NeedSetup 表示还没有任何账号。
func (s *Store) NeedSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users) == 0
}

// Setup 创建首个账号（仅在无账号时允许）。
func (s *Store) Setup(name, pass string) error {
	if len(name) < 2 || len(pass) < 6 {
		return fmt.Errorf("用户名至少 2 位，密码至少 6 位")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) > 0 {
		return fmt.Errorf("已初始化，不能重复注册")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	const iter = 120000
	hash := pbkdf2([]byte(pass), salt, iter, 32)
	u := User{
		Name: name,
		Salt: base64.StdEncoding.EncodeToString(salt),
		Hash: base64.StdEncoding.EncodeToString(hash),
		Iter: iter,
	}
	s.users = append(s.users, u)
	return s.saveLocked()
}

// Verify 校验用户名口令。
func (s *Store) Verify(name, pass string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Name != name {
			continue
		}
		salt, err1 := base64.StdEncoding.DecodeString(u.Salt)
		want, err2 := base64.StdEncoding.DecodeString(u.Hash)
		if err1 != nil || err2 != nil {
			return false
		}
		got := pbkdf2([]byte(pass), salt, u.Iter, len(want))
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	return false
}

// NewSession 生成会话令牌并绑定用户名。
func (s *Store) NewSession(user string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = session{user: user, exp: time.Now().Add(24 * time.Hour)}
	s.mu.Unlock()
	return token
}

// Valid 校验令牌，返回绑定的用户名。
func (s *Store) Valid(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return "", false
	}
	if time.Now().After(sess.exp) {
		delete(s.sessions, token)
		return "", false
	}
	return sess.user, true
}

// SetSessionUser 在改名后同步会话里的用户名。
func (s *Store) SetSessionUser(token, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[token]; ok {
		sess.user = user
		s.sessions[token] = sess
	}
}

// Logout 注销令牌。
func (s *Store) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(fileData{Users: s.users}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// pbkdf2 是 PBKDF2-HMAC-SHA256 的本地实现（避免额外依赖）。
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	var out []byte
	var buf [4]byte
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
