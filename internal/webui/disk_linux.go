//go:build linux

package webui

import "syscall"

// diskFree 返回磁盘可用字节数与总字节数（Linux）。
func diskFree(path string) (free, total int64) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0
	}
	return int64(fs.Bavail) * int64(fs.Bsize), int64(fs.Blocks) * int64(fs.Bsize)
}
