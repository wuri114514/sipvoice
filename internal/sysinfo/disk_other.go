//go:build !linux

package sysinfo

func disk() (total, free uint64) { return 0, 0 }
