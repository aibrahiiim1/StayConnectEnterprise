package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// P6 L7: lanes run concurrently, a running lane is never started twice, and wait returns only when all are done.
func TestLaneSchedulerRunsLanesIndependently(t *testing.T) {
	l := newLaneScheduler()
	slow := make(chan struct{})
	var fastDone atomic.Bool
	if !l.start("slow-pms", func() { <-slow }) {
		t.Fatal("first start refused")
	}
	if l.start("slow-pms", func() { t.Error("a running lane was started again") }) {
		t.Fatal("a running lane must not start twice")
	}
	done := make(chan struct{})
	l.start("fast-pms", func() { fastDone.Store(true); close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow lane held another lane")
	}
	waited := make(chan struct{})
	go func() { l.wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait returned while a lane was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(slow)
	<-waited
	if !l.start("slow-pms", func() {}) {
		t.Fatal("a finished lane must be startable again")
	}
	l.wait()
}
