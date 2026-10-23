package shape

// SharedFloorKbpsForTest exports the shared guaranteed-rate rule so the applier's tests need not duplicate it.
func SharedFloorKbpsForTest(groupKbps int) int { return sharedFloorKbps(groupKbps) }
