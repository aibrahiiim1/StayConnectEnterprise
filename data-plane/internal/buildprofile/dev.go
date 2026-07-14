//go:build devlicense

// This file is compiled ONLY into explicit development/test builds
// (`go build -tags devlicense`). Such a binary may run in permissive
// unlicensed-dev mode. It must never be shipped to a production appliance;
// the default build excludes it (see prod.go).
package buildprofile

// Production is false in a development build.
const Production = false

// Name identifies the build profile for logs/telemetry.
const Name = "development"
