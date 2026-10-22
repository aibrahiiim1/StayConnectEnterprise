package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunRetentionToEndWorksTheBacklogOffAndStopsWhenAPassChangesNothing(t *testing.T) {
	left := 1200
	pass := func(_ context.Context, fn string) (int, error) {
		if fn != "iam_v2.run_data_retention" || left <= 0 {
			return 0, nil
		}
		n := 500
		if left < n {
			n = left
		}
		left -= n
		return n, nil
	}
	staff := func(context.Context) (int, error) { return 0, nil }
	records, passes, err := runRetentionToEnd(context.Background(), pass, staff)
	if err != nil || records != 1200 || passes != 4 || left != 0 {
		t.Fatalf("records=%d passes=%d left=%d err=%v", records, passes, left, err)
	}
}

func TestRunRetentionToEndRefusesToCallItDoneWhenAnyFunctionFails(t *testing.T) {
	staff := func(context.Context) (int, error) { return 0, nil }
	for _, failing := range append(append([]string{}, retentionGuestFns...), "staff") {
		failing := failing
		pass := func(_ context.Context, fn string) (int, error) {
			if fn == failing {
				return 0, errors.New("boom")
			}
			return 0, nil
		}
		st := staff
		if failing == "staff" {
			st = func(context.Context) (int, error) { return 0, errors.New("boom") }
		}
		_, _, err := runRetentionToEnd(context.Background(), pass, st)
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s failing was reported as complete: %v", failing, err)
		}
	}
}

func TestRunRetentionToEndStopsWhenTheDeadlineEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	pass := func(ctx context.Context, _ string) (int, error) { time.Sleep(20 * time.Millisecond); return 1, nil } // never finishes
	_, _, err := runRetentionToEnd(ctx, pass, func(context.Context) (int, error) { return 0, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a run that never reaches the end ended with %v", err)
	}
}

func TestSumPassOutput(t *testing.T) {
	if n, err := sumPassOutput("inactive_identities|3\nexpired_records|4\n"); err != nil || n != 7 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := sumPassOutput(""); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := sumPassOutput("garbage"); err == nil {
		t.Fatal("an unreadable answer must not count as 'nothing changed'")
	}
}

func TestStagingOrphaned(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cases := map[string]bool{
		"restore_20261022t110000z": false, // an hour old: may be running
		"restore_20261022t050000z": true,  // seven hours: dead
		"restore_garbage":          false,
		pgDatabase:                 false,
		"restore_20261022T050000Z": false, // not the lowercase form restore.go creates
	}
	for name, want := range cases {
		if got := stagingOrphaned(name, now); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func TestTheRestoreDeadlineContainsTheRetentionBudgetAndTheStepsAroundIt(t *testing.T) {
	if got := restoreOverallDeadline(); got != restoreBeforeRetentionBudget+restoreRetentionDeadline+restoreAfterRetentionBudget {
		t.Fatalf("overall deadline %s", got)
	}
	if restoreOverallDeadline() >= stagingOrphanAfter {
		t.Fatalf("a running restore (%s) can outlive the age at which its staging database is called dead (%s)", restoreOverallDeadline(), stagingOrphanAfter)
	}
	// Full budget when the restore has all its time left.
	ctx, cancel := context.WithTimeout(context.Background(), restoreOverallDeadline())
	defer cancel()
	c, cancel2, ok := retentionContext(ctx)
	defer cancel2()
	dl, _ := c.Deadline()
	if !ok || time.Until(dl) < restoreRetentionDeadline-time.Minute {
		t.Fatalf("retention was not given its %s budget: ok=%v left=%s", restoreRetentionDeadline, ok, time.Until(dl))
	}
	// The steps after it keep their time even when the earlier steps overran.
	short, cancel3 := context.WithTimeout(context.Background(), restoreAfterRetentionBudget+10*time.Minute)
	defer cancel3()
	c2, cancel4, ok2 := retentionContext(short)
	defer cancel4()
	dl2, _ := c2.Deadline()
	if !ok2 || time.Until(dl2) > 10*time.Minute+time.Second {
		t.Fatalf("retention may eat the time the swap needs: ok=%v left=%s", ok2, time.Until(dl2))
	}
	// Nothing left to give: refused, not run.
	none, cancel5 := context.WithTimeout(context.Background(), restoreAfterRetentionBudget)
	defer cancel5()
	if _, cancel6, ok3 := retentionContext(none); ok3 {
		cancel6()
		t.Fatal("retention was started with no time to spare")
	} else {
		cancel6()
	}
}
