//go:build !linux

package ledger

// dirCapacity is unknown off Linux (the appliance is Linux; this keeps the package building on a workstation).
func dirCapacity(string) (free, total int64) { return 0, 0 }
