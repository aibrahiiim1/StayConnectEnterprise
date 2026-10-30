package main

import (
	"fmt"
	"net/http"
	"testing"
)

func TestEnforcementPolicy_OutageContinuityIsASettingWithBoundsAndZeroMeansOff(t *testing.T) {
	for _, v := range []int{0, 90, 120} {
		st := &fakeEnforcementStore{pol: shippedPolicy()}
		s := newEnfServer(st)
		body := fmt.Sprintf(`{"outage_continuity_minutes":%d,"reason":"agreed outage tolerance","password":"right-password"}`, v)
		code, resp := enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"hotel_it_manager"}), body)
		if code != 200 {
			t.Fatalf("value %d: code %d %s", v, code, resp)
		}
		if len(st.applied) != 1 || st.applied[0].Field != "outage_continuity_minutes" || st.applied[0].Value != fmt.Sprint(v) {
			t.Fatalf("value %d: applied %+v", v, st.applied)
		}
	}
	// The shipped default is 60; sending it again is "no change", not a write.
	st := &fakeEnforcementStore{pol: shippedPolicy()}
	s := newEnfServer(st)
	code, _ := enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"hotel_it_manager"}),
		`{"outage_continuity_minutes":60,"reason":"same value","password":"right-password"}`)
	if code != 400 || st.applyCall != 0 {
		t.Fatalf("unchanged value must not write: code %d calls %d", code, st.applyCall)
	}
	var found bool
	for _, d := range enforcementSettings() {
		if d.Field == "outage_continuity_minutes" {
			found = true
			if d.Default != 60 || d.Unit != "minutes" || d.Min == nil || *d.Min != 0 || d.Max == nil || *d.Max != 120 || len(d.Explanation) < 80 {
				t.Fatalf("setting documentation incomplete: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("the setting is not served with its documentation")
	}
}
