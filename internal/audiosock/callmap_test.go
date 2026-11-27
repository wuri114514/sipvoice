package audiosock

import (
	"os"
	"path/filepath"
	"testing"
)

// EnsureCallmapDir 只是 ensureWritableDir 的固定路径包装，这里测底层行为：
// 目录能自动创建、能写、且不留探测文件。
func TestEnsureWritableDir(t *testing.T) {
	base := t.TempDir()

	// 1) 已存在的可写目录：直接通过
	if err := ensureWritableDir(base); err != nil {
		t.Fatalf("可写目录应通过: %v", err)
	}
	// 2) 不该残留探测文件
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("探测文件没清干净: %v", entries)
	}

	// 3) 不存在的目录：自动创建
	sub := filepath.Join(base, "callmap")
	if err := ensureWritableDir(sub); err != nil {
		t.Fatalf("应自动创建目录: %v", err)
	}
	fi, err := os.Stat(sub)
	if err != nil || !fi.IsDir() {
		t.Fatalf("目录未创建: %v", err)
	}
}
