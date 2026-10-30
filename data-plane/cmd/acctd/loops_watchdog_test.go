package main

import (
	"context"
	"testing"
	"time"
)

// A submission that never returns must stop the beat, so the watchdog withholds its ping and systemd restarts acctd.
func TestSubmitterBeatStopsWhenASubmissionHangs(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s := newSubmitter(func() { <-release })
	if !s.alive(time.Second) {
		t.Fatal("a new submitter must count as alive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.loop(ctx, func() time.Duration { return 5 * time.Millisecond })
	time.Sleep(60 * time.Millisecond)
	if s.alive(30 * time.Millisecond) {
		t.Fatal("the beat kept going while a submission was stuck")
	}
}

func TestSubmitterBeatContinuesWhileTurning(t *testing.T) {
	s := newSubmitter(func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.loop(ctx, func() time.Duration { return 5 * time.Millisecond })
	time.Sleep(60 * time.Millisecond)
	if !s.alive(30 * time.Millisecond) {
		t.Fatal("the beat stopped although submissions complete")
	}
}
