package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// B-11: a reassignment while netd runs is adopted, and plans for the NEW scope are accepted without a restart.
func TestScopeChangeIsAdoptedWithoutARestart(t *testing.T) {
	p := liveWriter(newFakeTC())
	next := liveMode()
	next.SiteID = "11111111-2222-4333-8444-555566667777"
	next.AssignGen = 8
	w := scopeWatch{p: p, load: func(context.Context) (phase3Mode, error) { return next, nil }}

	env := envelope(1)
	env.SiteID = next.SiteID
	env.AssignmentGen = 8
	env.DesiredStateHash = shapeplan.HashDesiredState(env.ManagedBridges, env.Sessions)
	if _, err := p.submit(context.Background(), env, time.Now()); err == nil {
		t.Fatal("precondition: a plan for the new site is refused under the old scope")
	}
	if !w.check(context.Background()) {
		t.Fatal("the changed scope was not adopted")
	}
	env.PlanGeneration = 2
	env.DesiredStateHash = shapeplan.HashDesiredState(env.ManagedBridges, env.Sessions)
	if _, err := p.submit(context.Background(), env, time.Now()); err != nil {
		t.Fatalf("a plan for the adopted scope was refused: %v", err)
	}
	if w.check(context.Background()) {
		t.Fatal("an unchanged scope reported a change")
	}
}

// Going live at runtime needs what going live at start needs. Without an authenticated producer, or with the
// writer boundary missing, the appliance stays dark.
func TestScopeWatchGoingLiveRequiresProducerAndBoundary(t *testing.T) {
	dark := &phase3Shaping{shp: newFakeTC()}
	w := scopeWatch{p: dark, load: func(context.Context) (phase3Mode, error) { return liveMode(), nil }}
	if w.check(context.Background()) || dark.mode.Active {
		t.Fatal("went live with no authenticated producer")
	}
	dark.authz = shapingAuthz{allowedUID: testUID, configured: true}
	w.verifyBoundary = func(context.Context) error { return errors.New("guards missing") }
	if w.check(context.Background()) || dark.mode.Active {
		t.Fatal("went live without the writer boundary")
	}
	w.verifyBoundary = func(context.Context) error { return nil }
	if !w.check(context.Background()) || !dark.mode.Active {
		t.Fatal("a complete configuration did not go live")
	}
	// An unreadable configuration keeps the scope in force.
	w.load = func(context.Context) (phase3Mode, error) { return phase3Mode{}, errors.New("flags unreadable") }
	if w.check(context.Background()) || !dark.mode.Active {
		t.Fatal("an unreadable configuration changed the scope")
	}
}
