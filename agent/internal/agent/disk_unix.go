package agent

import "syscall"

func diskOf(dir string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
}
