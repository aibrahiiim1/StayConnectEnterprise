//go:build !windows

package pmsd

func isPlatformReset(error) bool { return false }
