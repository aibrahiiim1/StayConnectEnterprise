package shape

// SharedFloorKbpsForTest exposes the SHARED guaranteed-rate rule to the applier's tests.
//
// The rule belongs here, next to the class construction it decides, but the property worth asserting — that a
// shared plan is not quietly divided into per-device slices — is a property of the APPLIER's behaviour and is
// tested there. Rather than duplicate the arithmetic in two packages and let the copies drift, the one
// implementation is exported for the test that cares.
func SharedFloorKbpsForTest(groupKbps int) int { return sharedFloorKbps(groupKbps) }
