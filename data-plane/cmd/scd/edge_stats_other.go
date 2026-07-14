//go:build !linux

package main

// diskStats is Linux-only; appliances run Linux. Other platforms (dev
// builds) report unavailable.
func diskStats(string) (free, total int64) { return -1, -1 }
