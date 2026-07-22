//go:build linux

package main

// SO_PEERCRED is a Linux socket option: the kernel, not the caller, states which uid is on the other end. That
// is the whole point — a local process can forge any header it likes, but it cannot forge this.

import (
	"errors"
	"net"
	"syscall"
)

func peerCredentials(c net.Conn) (producerIdentity, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return producerIdentity{}, errors.New("caller is not connected over a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return producerIdentity{}, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return producerIdentity{}, err
	}
	if credErr != nil {
		return producerIdentity{}, credErr
	}
	if cred == nil {
		return producerIdentity{}, errors.New("peer credentials unavailable")
	}
	return producerIdentity{UID: int(cred.Uid), PID: int(cred.Pid)}, nil
}
