package main

// A pass explains itself: one log line per full pass with the time it waited for the previous pass, how much of its budget was left when it got
// the lock, and where its time went. Without it a slow pass under load is a guess.

import (
	"log/slog"
	"time"
)

type passTrace struct {
	last   time.Time
	stages []slog.Attr
	counts []slog.Attr
}

func newPassTrace() *passTrace { return &passTrace{last: time.Now()} }

// mark attributes the time since the previous mark to a stage; stages repeated per bridge accumulate under one name.
func (p *phase3Shaping) mark(stage string) {
	if p.trace == nil {
		return
	}
	now := time.Now()
	d := now.Sub(p.trace.last)
	p.trace.last = now
	for i, a := range p.trace.stages {
		if a.Key == stage {
			p.trace.stages[i] = slog.Int64(stage, a.Value.Int64()+d.Milliseconds())
			return
		}
	}
	p.trace.stages = append(p.trace.stages, slog.Int64(stage, d.Milliseconds()))
}

func (p *phase3Shaping) count(name string, n int) {
	if p.trace == nil || n == 0 {
		return
	}
	for i, a := range p.trace.counts {
		if a.Key == name {
			p.trace.counts[i] = slog.Int64(name, a.Value.Int64()+int64(n))
			return
		}
	}
	p.trace.counts = append(p.trace.counts, slog.Int64(name, int64(n)))
}
