//go:build windows

package pmsd

import (
	"errors"
	"syscall"
)

// Winsock reports a reset as WSAECONNRESET/WSAECONNABORTED, which are not the POSIX errnos.
func isPlatformReset(err error) bool {
	var en syscall.Errno
	return errors.As(err, &en) && (en == 10054 || en == 10053)
}
