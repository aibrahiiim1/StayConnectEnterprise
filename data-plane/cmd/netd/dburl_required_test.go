package main

import (
	"strings"
	"testing"
)

// AD-64: no built-in database URL with a published password.
func TestRequireDBURL_netd(t *testing.T) {
	for _, v := range []string{"", "   "} {
		err := requireDBURL(v)
		if err == nil || !strings.Contains(err.Error(), "NETD_DB_URL is not set") {
			t.Fatalf("a missing URL must stop netd with a clear message, got %v", err)
		}
	}
	if err := requireDBURL("postgres://svc:x@127.0.0.1:5432/stayconnect_site"); err != nil {
		t.Fatal(err)
	}
}
