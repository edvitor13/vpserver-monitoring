package host

import "syscall"

// statfs devolve total, livre e disponível (para não-root) do sistema de arquivos.
func statfs(path string) (total, free, avail uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bfree * bs, st.Bavail * bs
}
