//go:build !linux

package main

import "os"

type fileIdentity struct{}

// identityOf has no ctime to trust off Linux, so nothing is cached and every digest is computed.
func identityOf(os.FileInfo) (fileIdentity, bool) { return fileIdentity{}, false }
