//go:build !stayconnect_production

package iamv2

// productionBuild is false unless the binary is built with `-tags stayconnect_production`.
//
// The DEVELOPMENT appliance and every test build land here, and keep the configurable guest-authority
// behaviour they were accepted with. That is deliberate: the DEVELOPMENT appliance deliberately exercises
// both authorities and its accepted evidence depends on being able to.
const productionBuild = false
