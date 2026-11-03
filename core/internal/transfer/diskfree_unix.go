//go:build unix

package transfer

import "syscall"

// freeBytes returns available bytes on the filesystem holding dir, or -1.
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
