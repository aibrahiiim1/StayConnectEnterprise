//go:build !stayconnect_production

package iamv2

// productionBuild is false unless built with `-tags stayconnect_production`; development and test builds keep
// the configurable guest-authority behaviour.
const productionBuild = false
