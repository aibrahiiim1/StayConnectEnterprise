//go:build stayconnect_production

package iamv2

// productionBuild is a build property, not configuration: whoever can edit an env file must not be able to
// change which IAM authority is in force.
const productionBuild = true
