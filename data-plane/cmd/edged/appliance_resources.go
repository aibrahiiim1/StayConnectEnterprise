package main

// Appliance load, memory, uptime and disk come from /proc and statfs(2); nothing is shelled out to.
// The Postgres data volume is not reported: no configuration names its host path, so it is not guessed.
// Parsing is platform-independent; only collection is Linux-specific.

import (
	"bufio"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type loadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
	CPUs    int     `json:"cpus"`
	// PerCPU is the one-minute load divided by the CPU count.
	PerCPU float64 `json:"one_per_cpu"`
}

type memoryFigures struct {
	TotalBytes     int64   `json:"total_bytes"`
	AvailableBytes int64   `json:"available_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	UsedPct        float64 `json:"used_pct"`
}

type diskFigures struct {
	Path           string  `json:"path"`
	TotalBytes     int64   `json:"total_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	AvailableBytes int64   `json:"available_bytes"`
	UsedPct        float64 `json:"used_pct"`
	// SameFilesystemAs names an earlier path on the same filesystem so one disk is not shown twice.
	SameFilesystemAs string `json:"same_filesystem_as,omitempty"`
}

type applianceResources struct {
	section
	CollectedAt   time.Time      `json:"collected_at"`
	Load          *loadAverage   `json:"load,omitempty"`
	Memory        *memoryFigures `json:"memory,omitempty"`
	UptimeSeconds *int64         `json:"uptime_seconds,omitempty"`
	Disks         []diskFigures  `json:"disks"`
	Errors        []string       `json:"errors,omitempty"`
}

var resourcePaths = []string{"/", "/opt/stayconnect"}

func parseLoadavg(s string) (one, five, fifteen float64, err error) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, 0, errors.New("loadavg: expected at least three fields")
	}
	vals := make([]float64, 3)
	for i := 0; i < 3; i++ {
		v, e := strconv.ParseFloat(f[i], 64)
		if e != nil || v < 0 {
			return 0, 0, 0, errors.New("loadavg: field is not a load figure")
		}
		vals[i] = v
	}
	return vals[0], vals[1], vals[2], nil
}

// parseMeminfo: "used" is total minus MemAvailable, not minus free, or page cache makes a healthy appliance
// look full. A kernel without MemAvailable (pre-3.14) is reported unreadable, not approximated.
func parseMeminfo(s string) (memoryFigures, error) {
	var total, avail int64 = -1, -1
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			v *= 1024
		}
		switch key {
		case "MemTotal":
			total = v
		case "MemAvailable":
			avail = v
		}
	}
	if total <= 0 || avail < 0 {
		return memoryFigures{}, errors.New("meminfo: MemTotal or MemAvailable missing")
	}
	if avail > total {
		avail = total
	}
	used := total - avail
	return memoryFigures{TotalBytes: total, AvailableBytes: avail, UsedBytes: used, UsedPct: round1(float64(used) / float64(total) * 100)}, nil
}

func parseUptime(s string) (int64, error) {
	f := strings.Fields(s)
	if len(f) < 1 {
		return 0, errors.New("uptime: empty")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0, errors.New("uptime: not a number")
	}
	return int64(v), nil
}

// diskFromStatfs matches `df`: percentage is used / (used + bavail), so root-reserved blocks count as unavailable.
func diskFromStatfs(path string, blocks, bfree, bavail uint64, bsize int64) (diskFigures, error) {
	if bsize <= 0 || blocks == 0 || bfree > blocks || bavail > blocks {
		return diskFigures{}, errors.New("statfs: implausible figures for " + path)
	}
	total := int64(blocks) * bsize
	used := int64(blocks-bfree) * bsize
	avail := int64(bavail) * bsize
	pct := 0.0
	if used+avail > 0 {
		pct = round1(float64(used) / float64(used+avail) * 100)
	}
	return diskFigures{Path: path, TotalBytes: total, UsedBytes: used, AvailableBytes: avail, UsedPct: pct}, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func (s *server) reportsApplianceResources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, collectApplianceResources())
}
