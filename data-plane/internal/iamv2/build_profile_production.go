//go:build stayconnect_production

package iamv2

// productionBuild is true only when the binary is built with `-tags stayconnect_production`, which is how a
// Production appliance is built (docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md §2).
//
// This is a BUILD property, not a configuration one, and that is the whole point: a setting that decides
// which IAM authority is in force can be changed by whoever can edit an env file. The Production binary
// carries the answer inside it.
const productionBuild = true
