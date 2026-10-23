//go:build !linux

package main

// No SO_PEERCRED off Linux, so this fails closed: a build that cannot authenticate its producer must not mutate shaping.

import (
	"errors"
	"net"
)

func peerCredentials(c net.Conn) (producerIdentity, error) {
	return producerIdentity{}, errors.New("peer credentials are not available on this platform")
}
