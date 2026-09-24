package main

import (
	"runtime"
	"testing"
)

func TestParseLoadavg(t *testing.T) {
	one, five, fifteen, err := parseLoadavg("0.52 0.58 0.61 1/467 12345\n")
	if err != nil || one != 0.52 || five != 0.58 || fifteen != 0.61 {
		t.Fatalf("got %v %v %v %v", one, five, fifteen, err)
	}
	if _, _, _, err := parseLoadavg("garbage"); err == nil {
		t.Error("a malformed loadavg was accepted")
	}
}

func TestParseMeminfoUsesAvailableNotFree(t *testing.T) {
	sample := `MemTotal:        8000000 kB
MemFree:          200000 kB
MemAvailable:    6000000 kB
Buffers:          100000 kB
Cached:          5000000 kB
`
	m, err := parseMeminfo(sample)
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalBytes != 8000000*1024 || m.AvailableBytes != 6000000*1024 {
		t.Fatalf("figures = %+v", m)
	}
	// Total minus FREE would say 97.5% used on a machine whose page cache is simply doing its job.
	if m.UsedPct != 25 {
		t.Errorf("used = %v%%, want 25%% (total minus available)", m.UsedPct)
	}
	if _, err := parseMeminfo("MemTotal: 8000000 kB\nMemFree: 1 kB\n"); err == nil {
		t.Error("a kernel without MemAvailable must be reported as unreadable, not approximated")
	}
}

func TestParseUptime(t *testing.T) {
	if u, err := parseUptime("350735.47 234388.90\n"); err != nil || u != 350735 {
		t.Fatalf("uptime = %d, %v", u, err)
	}
	if _, err := parseUptime(""); err == nil {
		t.Error("empty uptime accepted")
	}
}

func TestDiskFiguresMatchDf(t *testing.T) {
	// 100 blocks of 4 KiB: 30 free, of which 25 available to unprivileged writers (5 reserved for root).
	d, err := diskFromStatfs("/", 100, 30, 25, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalBytes != 409600 || d.UsedBytes != 70*4096 || d.AvailableBytes != 25*4096 {
		t.Fatalf("figures = %+v", d)
	}
	// df: used / (used + avail) = 70 / 95
	if d.UsedPct != 73.7 {
		t.Errorf("used = %v%%, want 73.7%% (the reserved blocks are not free space anyone can write to)", d.UsedPct)
	}
	if _, err := diskFromStatfs("/", 0, 0, 0, 4096); err == nil {
		t.Error("an empty filesystem report was accepted")
	}
	if _, err := diskFromStatfs("/", 10, 20, 5, 4096); err == nil {
		t.Error("more free blocks than blocks was accepted")
	}
}

func TestResourcesAreUnavailableOffLinuxRatherThanInvented(t *testing.T) {
	r := collectApplianceResources()
	if runtime.GOOS != "linux" {
		if r.Available || r.Reason != "unavailable_on_this_platform" || r.Load != nil || r.Memory != nil {
			t.Fatalf("a non-Linux host reported figures: %+v", r)
		}
		return
	}
	if !r.Available {
		t.Skipf("resources unreadable in this Linux environment: %v", r.Errors)
	}
}
