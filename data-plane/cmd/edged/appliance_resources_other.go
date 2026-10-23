//go:build !linux

package main

import "time"

// Off Linux the figures would describe a workstation, not the appliance, so none are reported.
func collectApplianceResources() applianceResources {
	return applianceResources{
		section:     section{Available: false, Reason: "unavailable_on_this_platform"},
		CollectedAt: time.Now().UTC(),
		Disks:       []diskFigures{},
	}
}
