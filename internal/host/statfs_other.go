//go:build !linux

package host

// statfs só existe no Linux; em outros sistemas (desenvolvimento) fica zerado.
func statfs(string) (total, free, avail uint64) { return }
