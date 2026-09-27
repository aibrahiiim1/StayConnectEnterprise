//go:build !stayconnect_production

package buildprofile

// stayconnectProduction is false unless the binary is built with `-tags stayconnect_production` (see scope.go).
const stayconnectProduction = false
