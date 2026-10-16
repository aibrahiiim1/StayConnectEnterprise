package main

import (
	"errors"
	"os/user"
	"testing"
)

func fakeGroups(names ...string) func(string) (*user.Group, error) {
	return func(n string) (*user.Group, error) {
		for i, x := range names {
			if x == n {
				return &user.Group{Name: n, Gid: string(rune('1' + i))}, nil
			}
		}
		return nil, user.UnknownGroupError(n)
	}
}

func TestSocketGroupPrefersTheDedicatedGroup(t *testing.T) {
	g, err := lookupSocketGroup("", fakeGroups("stayconnect", "stayconnect-scd"))
	if err != nil || g.Name != "stayconnect-scd" {
		t.Fatalf("got %v %v, want stayconnect-scd", g, err)
	}
}

func TestSocketGroupFallsBackToTheLegacyGroupBeforeProvisioning(t *testing.T) {
	g, err := lookupSocketGroup("", fakeGroups("stayconnect"))
	if err != nil || g.Name != "stayconnect" {
		t.Fatalf("got %v %v, want the legacy group", g, err)
	}
}

func TestAMisspelledConfiguredGroupIsAnErrorNotAWiderGroup(t *testing.T) {
	_, err := lookupSocketGroup("stayconect-scd", fakeGroups("stayconnect", "stayconnect-scd"))
	var unk user.UnknownGroupError
	if !errors.As(err, &unk) {
		t.Fatalf("err = %v, want UnknownGroupError", err)
	}
}
