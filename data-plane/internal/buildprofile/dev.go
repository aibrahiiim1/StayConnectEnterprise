//go:build devlicense

// Compiled only with `-tags devlicense`; such a binary may run unlicensed and must never ship to an appliance.
package buildprofile

const Production = false

const Name = "development"
