package main

import (
	"testing"
	"time"
)

// ON THE WI-FI means an assigned, unexpired lease -- not a declined one, not an expired one.
func TestParseActiveLeases(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw := []byte(`{"leases":[
	  {"ip-address":"192.168.77.129","hw-address":"8A:E7:C8:91:C1:A3","hostname":"iphone","state":0,"cltt":1799999000,"valid-lft":1800},
	  {"ip-address":"192.168.77.130","hw-address":"02:00:00:00:00:01","state":0,"cltt":1799990000,"valid-lft":1800},
	  {"ip-address":"192.168.77.131","hw-address":"02:00:00:00:00:02","state":1,"cltt":1799999000,"valid-lft":1800}]}`)
	got := parseActiveLeases(raw, now)
	if len(got) != 1 {
		t.Fatalf("active leases = %v", got)
	}
	l, ok := got["8a:e7:c8:91:c1:a3"]
	if !ok || l.IP != "192.168.77.129" || l.Hostname != "iphone" || !l.Expires.After(now) {
		t.Fatalf("lease = %+v", l)
	}
	d := deviceRow{MAC: "8a:e7:c8:91:c1:a3"}
	d.withLease(got)
	if !d.OnNetwork || d.LeaseIP != "192.168.77.129" || d.Online {
		t.Fatalf("on the Wi-Fi is not signed in: %+v", d)
	}
	if parseActiveLeases([]byte(`not json`), now) != nil {
		t.Fatal("an unreadable answer is no leases")
	}
}
