//go:build !linux

package webui

// diskFree 非 Linux 平台不统计（面板只在 Linux 上部署）。
func diskFree(path string) (free, total int64) { return 0, 0 }
