//go:build !linux

package main

// THE PEER CHECK IS LINUX-ONLY, AND THIS FILE SAYS SO OUT LOUD RATHER THAN QUIETLY ALLOWING EVERYTHING.
//
// SO_PEERCRED and /proc/<pid>/exe are Linux interfaces. scd runs on the appliance, which is Linux; this
// build exists so the package compiles and its unit tests run on a workstation.
//
// requireEdgedPeer therefore ALLOWS here. That is safe only because of what this build is: a non-Linux
// binary is not the appliance, and scd's socket is not reachable from a network on either. If this package
// ever gains a non-Linux deployment target, this file is the thing that has to change first -- which is why
// it is a visible stub with a reason instead of a build tag on the caller.

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

func slogWarnPeerRefused(_ peerCred, _ string) {}
