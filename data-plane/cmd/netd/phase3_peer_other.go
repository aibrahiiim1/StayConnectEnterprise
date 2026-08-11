//go:build !linux

package main

// On a non-Linux development host there is no SO_PEERCRED, so there is no way to know who is calling. The stub
// therefore fails CLOSED rather than pretending the caller is authorized: netd only runs on the appliance, and
// a build that cannot authenticate its producer must not be able to mutate shaping.

import (
	"errors"
	"net"
)

func peerCredentials(c net.Conn) (producerIdentity, error) {
	return producerIdentity{}, errors.New("peer credentials are not available on this platform")
}
