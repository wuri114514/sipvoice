package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
)

const iterCount = 120000

func makeUser(name, pass string) (User, error) {
	if len(name) < 2 || len(pass) < 6 {
		return User{}, fmt.Errorf("用户名至少 2 位，密码至少 6 位")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return User{}, err
	}
	hash := pbkdf2([]byte(pass), salt, iterCount, 32)
	return User{
		Name: name,
		Salt: base64.StdEncoding.EncodeToString(salt),
		Hash: base64.StdEncoding.EncodeToString(hash),
		Iter: iterCount,
	}, nil
}

// List 返回所有用户名（有序）。
func (s *Store) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u.Name)
	}
	sort.Strings(out)
	return out
}

// Add 新增账号。
func (s *Store) Add(name, pass string) error {
	u, err := makeUser(name, pass)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.users {
		if x.Name == name {
			return fmt.Errorf("用户名已存在")
		}
	}
	s.users = append(s.users, u)
	return s.saveLocked()
}

// Delete 删除账号（至少保留一个）。
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) <= 1 {
		return fmt.Errorf("至少保留一个账号")
	}
	for i, u := range s.users {
		if u.Name == name {
			s.users = append(s.users[:i], s.users[i+1:]...)
			return s.saveLocked()
		}
	}
	return fmt.Errorf("账号不存在")
}

// Rename 修改用户名。
func (s *Store) Rename(old, newName string) error {
	if len(newName) < 2 {
		return fmt.Errorf("用户名至少 2 位")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Name == newName {
			return fmt.Errorf("用户名已存在")
		}
	}
	for i := range s.users {
		if s.users[i].Name == old {
			s.users[i].Name = newName
			return s.saveLocked()
		}
	}
	return fmt.Errorf("账号不存在")
}

// SetPassword 修改密码。
func (s *Store) SetPassword(name, pass string) error {
	if len(pass) < 6 {
		return fmt.Errorf("密码至少 6 位")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].Name == name {
			u, err := makeUser(name, pass)
			if err != nil {
				return err
			}
			u.Name = name
			s.users[i] = u
			return s.saveLocked()
		}
	}
	return fmt.Errorf("账号不存在")
}
