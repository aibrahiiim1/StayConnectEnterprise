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
