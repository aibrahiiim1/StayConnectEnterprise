//go:build !linux

package main

// The peer check is Linux-only (SO_PEERCRED, /proc/<pid>/exe), so this build allows: it exists so the package compiles and
// unit tests run on a workstation, which is not the appliance. If a non-Linux deployment target ever appears, this stub must change first.

import (
	"context"
	"net"
	"net/http"
)

type peerCred struct {
	PID  int32
	UID  uint32
	Exe  string
	Read bool
}

func withPeerCred(ctx context.Context, _ net.Conn) context.Context { return ctx }

func requireEdgedPeer(_ http.ResponseWriter, _ *http.Request) bool { return true }

// requireAdminPeer allows here for the same reason: a workstation build is not the appliance.
func requireAdminPeer(_ http.ResponseWriter, _ *http.Request) bool { return true }

func slogWarnPeerRefused(_ peerCred, _ string) {}
