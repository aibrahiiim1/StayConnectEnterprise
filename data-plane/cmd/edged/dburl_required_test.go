package main

import (
	"strings"
	"testing"
)

// AD-64: no built-in database URL with a published password.
func TestRequireDBURL_edged(t *testing.T) {
	t.Setenv("EDGED_DB_URL", "")
	c := loadCfg()
	if c.DBURL != "" {
		t.Fatalf("loadCfg must not invent a database URL")
	}
	err := requireDBURL(c)
	if err == nil || !strings.Contains(err.Error(), "EDGED_DB_URL is not set") {
		t.Fatalf("a missing URL must stop the service with a clear message, got %v", err)
	}
	t.Setenv("EDGED_DB_URL", "postgres://svc:x@127.0.0.1:5432/stayconnect_site")
	if err := requireDBURL(loadCfg()); err != nil {
		t.Fatalf("a set URL must be accepted: %v", err)
	}
}
