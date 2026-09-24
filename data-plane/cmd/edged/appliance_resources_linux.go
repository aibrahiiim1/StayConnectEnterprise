//go:build linux

package main

import (
	"math"
	"os"
	"runtime"
	"syscall"
	"time"
)

// collectApplianceResources reads the kernel's own accounting. Each figure is independent: one that cannot be
// read is recorded in Errors and the rest are still returned.
func collectApplianceResources() applianceResources {
	out := applianceResources{CollectedAt: time.Now().UTC(), Disks: []diskFigures{}}

	if b, err := os.ReadFile("/proc/loadavg"); err != nil {
		out.Errors = append(out.Errors, "load average could not be read")
	} else if one, five, fifteen, err := parseLoadavg(string(b)); err != nil {
		out.Errors = append(out.Errors, "load average could not be parsed")
	} else {
		cpus := runtime.NumCPU()
		la := &loadAverage{One: one, Five: five, Fifteen: fifteen, CPUs: cpus}
		if cpus > 0 {
			la.PerCPU = math.Round(one/float64(cpus)*100) / 100
		}
		out.Load = la
	}

	if b, err := os.ReadFile("/proc/meminfo"); err != nil {
		out.Errors = append(out.Errors, "memory could not be read")
	} else if m, err := parseMeminfo(string(b)); err != nil {
		out.Errors = append(out.Errors, "memory could not be parsed")
	} else {
		out.Memory = &m
	}

	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if u, err := parseUptime(string(b)); err == nil {
			out.UptimeSeconds = &u
		}
	}

	seen := map[[2]int32]string{}
	for _, p := range resourcePaths {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			// A path that does not exist on this unit (a development box without /opt/stayconnect) is simply
			// not reported.
			continue
		}
		d, err := diskFromStatfs(p, st.Blocks, st.Bfree, st.Bavail, int64(st.Bsize))
		if err != nil {
			out.Errors = append(out.Errors, "disk figures for "+p+" are implausible")
			continue
		}
		key := [2]int32{st.Fsid.X__val[0], st.Fsid.X__val[1]}
		if first, ok := seen[key]; ok {
			d.SameFilesystemAs = first
		} else {
			seen[key] = p
		}
		out.Disks = append(out.Disks, d)
	}

	out.section = section{Available: out.Load != nil || out.Memory != nil || len(out.Disks) > 0}
	if !out.Available {
		out.Reason = "resources_unreadable"
	}
	return out
}
