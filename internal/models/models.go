// Package models 管理本地语音模型文件：目录浏览、下载、上传、激活、删除。
package models

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Catalog 是可一键下载的 whisper.cpp 模型（ggml 格式）。
type CatalogEntry struct {
	Name string
	Desc string
	URL  string
	Size int64
}

// Catalog 按体积从小到大。
var Catalog = []CatalogEntry{
	{"ggml-tiny.bin", "tiny · 最快，准确率低", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin", 75 << 20},
	{"ggml-base.bin", "base · 小，较快", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-base.bin", 142 << 20},
	{"ggml-small.bin", "small · 推荐，准确率/速度平衡", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-small.bin", 466 << 20},
	{"ggml-medium.bin", "medium · 准确率高，较慢", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin", 1500 << 20},
	{"ggml-large-v3-turbo.bin", "large-v3-turbo · 大模型加速版", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin", 1600 << 20},
	{"ggml-base-q5_1.bin", "base-q5 · 量化版，小而快", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-base-q5_1.bin", 57 << 20},
	{"ggml-small-q5_1.bin", "small-q5 · 量化版，推荐", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-small-q5_1.bin", 181 << 20},
	{"ggml-medium-q5_0.bin", "medium-q5 · 量化版，准确率高", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-medium-q5_0.bin", 539 << 20},
	{"ggml-large-v3-turbo-q5_0.bin", "large-v3-turbo-q5 · 大模型量化版", "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin", 574 << 20},
}

// Entry 是面板展示的一条模型。
type Entry struct {
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Installed bool    `json:"installed"`
	Active    bool    `json:"active"`
	Busy      bool    `json:"busy"`
	Progress  float64 `json:"progress"`
	Desc      string  `json:"desc"`
	Catalog   bool    `json:"catalog"`
}

// Manager 管理模型目录。
type Manager struct {
	dir string
	mu  sync.Mutex
	dl  map[string]*download
}

type download struct {
	got, total int64
	done       bool
	err        string
}

// New 创建管理器。
func New(dir string) *Manager {
	_ = os.MkdirAll(dir, 0o755)
	return &Manager{dir: dir, dl: map[string]*download{}}
}

// Dir 返回模型目录。
func (m *Manager) Dir() string { return m.dir }

// List 列出目录内与目录外的模型。
func (m *Manager) List(active string) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	files, _ := os.ReadDir(m.dir)
	seen := map[string]bool{}
	var out []Entry
	activeBase := filepath.Base(active)

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".bin") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		seen[f.Name()] = true
		e := Entry{
			Name:      f.Name(),
			Size:      info.Size(),
			Installed: true,
			Active:    f.Name() == activeBase,
			Desc:      "本地上传/已有",
			Catalog:   false,
		}
		if d, ok := m.dl[f.Name()]; ok {
			e.Busy = d.got > 0 && !d.done
			if d.total > 0 {
				e.Progress = float64(d.got) / float64(d.total)
			}
		}
		out = append(out, e)
	}
	for _, c := range Catalog {
		if seen[c.Name] {
			continue
		}
		e := Entry{Name: c.Name, Size: c.Size, Desc: c.Desc, Catalog: true, Active: c.Name == activeBase}
		if d, ok := m.dl[c.Name]; ok {
			e.Busy = !d.done
			if d.total > 0 {
				e.Progress = float64(d.got) / float64(d.total)
			} else {
				e.Progress = 0
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// Download 下载目录中的模型；重复调用会复用进行中的任务。
func (m *Manager) Download(ctx context.Context, name string) error {
	var ce *CatalogEntry
	for i := range Catalog {
		if Catalog[i].Name == name {
			ce = &Catalog[i]
			break
		}
	}
	if ce == nil {
		return fmt.Errorf("不在可下载目录中: %s", name)
	}
	if _, err := os.Stat(filepath.Join(m.dir, name)); err == nil {
		return fmt.Errorf("已存在: %s", name)
	}

	m.mu.Lock()
	if d, ok := m.dl[name]; ok && !d.done {
		m.mu.Unlock()
		return nil
	}
	d := &download{}
	m.dl[name] = d
	m.mu.Unlock()

	go func() {
		err := m.fetch(ctx, ce, d)
		m.mu.Lock()
		d.done = true
		if err != nil {
			d.err = err.Error()
		}
		m.mu.Unlock()
		if err != nil {
			log.Printf("[models] 下载 %s 失败: %v", ce.Name, err)
		} else {
			log.Printf("[models] 下载 %s 完成", ce.Name)
		}
	}()
	return nil
}

func (m *Manager) fetch(ctx context.Context, ce *CatalogEntry, d *download) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ce.URL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	m.mu.Lock()
	d.total = resp.ContentLength
	m.mu.Unlock()

	tmp := filepath.Join(m.dir, ce.Name+".part")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 256*1024)
	var got int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			got += int64(n)
			m.mu.Lock()
			d.got = got
			m.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.dir, ce.Name))
}

// Delete 删除一个模型文件。
func (m *Manager) Delete(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("非法文件名")
	}
	return os.Remove(filepath.Join(m.dir, name))
}

// Upload 保存上传的模型文件，返回文件名。
func (m *Manager) Upload(r io.Reader, filename string) (string, error) {
	base := filepath.Base(filename)
	if !strings.HasSuffix(base, ".bin") {
		return "", fmt.Errorf("只接受 .bin 模型文件")
	}
	if strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("非法文件名")
	}
	dst := filepath.Join(m.dir, base)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return base, nil
}

// Status 返回某模型的下载状态。
func (m *Manager) Status(name string) (progress float64, busy bool, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.dl[name]
	if !ok {
		return 0, false, ""
	}
	if d.total > 0 {
		progress = float64(d.got) / float64(d.total)
	}
	return progress, !d.done, d.err
}

var _ = time.Second
