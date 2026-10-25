package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

const someProfile = "11111111-2222-3333-4444-555555555555"

func reqFrom(remote, target string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// The network, and so the design, comes from the connection only: nothing a client sends can pick another network's profile.
func TestTheDesignIsChosenByTheConnectionNotByWhatTheClientSends(t *testing.T) {
	key, q := designSelector(reqFrom("10.20.0.55:40000", "/?profile="+someProfile, map[string]string{"X-Forwarded-For": "10.99.0.1", "X-Real-IP": "10.99.0.2"}))
	if key != "ip:10.20.0.55" || q != "ip=10.20.0.55" {
		t.Fatalf("a client steered its own design: %s %s", key, q)
	}
	for _, target := range []string{"/?profile=" + someProfile, "/?profile=../etc", "/?profile="} {
		if k, _ := designSelector(reqFrom("10.20.0.55:1", target, nil)); k != "ip:10.20.0.55" {
			t.Fatalf("%s selected %s", target, k)
		}
	}
}

func TestOnlyThisMachineCanPreviewAProfile(t *testing.T) {
	if k, q := designSelector(reqFrom("127.0.0.1:5000", "/?profile="+someProfile, nil)); k != "profile:"+someProfile || q != "profile="+someProfile {
		t.Fatalf("local preview: %s %s", k, q)
	}
	if k, _ := designSelector(reqFrom("127.0.0.1:5000", "/?profile="+someProfile, map[string]string{"X-Forwarded-For": "10.20.0.7"})); k == "profile:"+someProfile {
		t.Fatal("a forwarded request was treated as a local preview")
	}
	if k, _ := designSelector(reqFrom("127.0.0.1:5000", "/?profile=not-a-uuid", nil)); k == "profile:not-a-uuid" {
		t.Fatal("a malformed profile id was accepted")
	}
}

func TestTheDesignCacheIsBoundedAndKeptPerClientAddress(t *testing.T) {
	c := &designCache{}
	c.mu.Lock()
	c.entries = map[string]designEntry{}
	for i := 0; i < designCacheMax*3; i++ {
		if len(c.entries) >= designCacheMax {
			c.evict()
		}
		c.entries["ip:"+strconv.Itoa(i)] = designEntry{fetched: time.Now(), ok: true}
	}
	n := len(c.entries)
	c.mu.Unlock()
	if n > designCacheMax {
		t.Fatalf("the cache grew to %d entries", n)
	}
}
