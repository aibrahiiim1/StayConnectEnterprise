//go:build !linux

package main

import "time"

// collectApplianceResources has nothing truthful to report off Linux: the appliance is a Linux machine, and a
// development workstation's figures would describe the workstation. So it says so.
func collectApplianceResources() applianceResources {
	return applianceResources{
		section:     section{Available: false, Reason: "unavailable_on_this_platform"},
		CollectedAt: time.Now().UTC(),
		Disks:       []diskFigures{},
	}
}
