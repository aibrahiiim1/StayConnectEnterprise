//go:build !devlicense

// Package buildprofile carries the COMPILE-TIME licensing profile. The default
// build (no build tags) is PRODUCTION: permissive unlicensed-dev mode is
// physically absent from the binary and cannot be enabled by any environment
// variable, config file, or runtime flag. A development/test build must be
// produced explicitly with `-tags devlicense` (see dev.go), which is the only
// way to obtain a binary that can run without a real signed license.
package buildprofile

// Production is true in the default (production) build.
const Production = true

// Name identifies the build profile for logs/telemetry.
const Name = "production"
