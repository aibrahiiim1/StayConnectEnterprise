package api

import (
	"strings"
	"testing"
)

func TestValidUserEmail(t *testing.T) {
	for _, ok := range []string{"grace@central.test", "grace.hopper+ops@mail.central.test", "a@b.co"} {
		if !validUserEmail(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{
		"", "grace", "@central.test", "grace@", "grace@central", "grace@.central.test", "grace@central.test.",
		"grace@central..test", "grace hopper@central.test", "Grace <grace@central.test>", "grace@central.test, x@y.z",
		"grace@central.test\n", "\"grace\"@central.test", "a@b@central.test", strings.Repeat("a", 250) + "@b.co",
	} {
		if validUserEmail(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
