//go:build !devlicense

// Package buildprofile is the compile-time licensing profile. The default build is production: unlicensed-dev
// mode is absent from the binary and cannot be enabled by env, config or flag; only `-tags devlicense` enables it.
package buildprofile

const Production = true

const Name = "production"
