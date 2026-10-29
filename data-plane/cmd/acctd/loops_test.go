package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCutoffIsClampedToTheRangeTheDatabaseEnforces(t *testing.T) {
	for in, want := range map[int]int{-4: 1, 0: 1, 1: 1, 5: 5, 60: 60, 61: 60, 5000: 60} {
		if got := clampCutoff(in); got != want {
			t.Errorf("clampCutoff(%d) = %d, want %d", in, got, want)
		}
	}
}

// netd renews a lease only under a current plan (90 s), so the submitter must not follow a long cut-off period.
func TestThePlanIsSubmittedOftenEnoughForNetdsLeasesWhateverTheCutoff(t *testing.T) {
	for _, secs := range []int{1, 5, 10, 30, 60} {
		got := submitPeriod(time.Duration(secs) * time.Second)
		if got > time.Duration(maxTickSeconds)*time.Second {
			t.Errorf("cutoff %ds submits every %v", secs, got)
		}
		if secs <= maxTickSeconds && got != time.Duration(secs)*time.Second {
			t.Errorf("cutoff %ds should submit every %ds, got %v", secs, secs, got)
		}
	}
	if 3*submitPeriod(60*time.Second) > planValidity {
		t.Fatal("fewer than three submissions fit inside the plan's validity at the largest cut-off")
	}
}

type fakeRow struct {
	v   int
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int)) = r.v
	return nil
}

type fakeDB struct{ row fakeRow }

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return f.row }

func TestReadCutoffMeansTheSettingOrNothing(t *testing.T) {
	ctx := context.Background()
	if s, found, err := readCutoffSeconds(ctx, &fakeDB{fakeRow{v: 9}}); s != 9 || !found || err != nil {
		t.Fatalf("%d %v %v", s, found, err)
	}
	if s, _, _ := readCutoffSeconds(ctx, &fakeDB{fakeRow{v: 500}}); s != 60 {
		t.Fatalf("an out-of-range stored value was not clamped: %d", s)
	}
	for name, e := range map[string]error{"no row": pgx.ErrNoRows, "no table": &pgconn.PgError{Code: "42P01"}} {
		if _, found, err := readCutoffSeconds(ctx, &fakeDB{fakeRow{err: e}}); found || err != nil {
			t.Errorf("%s: found=%v err=%v (an older schema is not an error)", name, found, err)
		}
	}
	if _, _, err := readCutoffSeconds(ctx, &fakeDB{fakeRow{err: errors.New("conn refused")}}); err == nil {
		t.Fatal("a database failure was swallowed")
	}
}

func TestTheClockFollowsTheSettingAndKeepsItsPeriodWhenTheReadFails(t *testing.T) {
	c := newCutoffClock(time.Second) // before any read: the earlier behaviour
	db := &fakeDB{fakeRow{v: 5}}
	ctx, cancel := context.WithCancel(context.Background())
	go c.watch(ctx, db, 5*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for c.period() != 5*time.Second && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if c.period() != 5*time.Second {
		t.Fatalf("the setting was not applied: %v", c.period())
	}
	db.row = fakeRow{err: errors.New("db down")}
	time.Sleep(30 * time.Millisecond)
	if c.period() != 5*time.Second {
		t.Fatalf("a failed read changed the period to %v", c.period())
	}
	db.row = fakeRow{err: pgx.ErrNoRows}
	time.Sleep(30 * time.Millisecond)
	cancel()
	if c.period() != 5*time.Second {
		t.Fatalf("a missing row changed the period to %v", c.period())
	}
}

func TestSubmitterCoalescesKicksAndNeverOverlaps(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	release := make(chan struct{})
	s := newSubmitter(func() {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		<-release
		running.Add(-1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.loop(ctx, func() time.Duration { return time.Hour })
	s.kick()
	for runs.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 50; i++ { // 50 requests while one submission is still running
		s.kick()
	}
	release <- struct{}{} // first finishes
	for runs.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	release <- struct{}{}
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 2 {
		t.Fatalf("50 kicks during one submission produced %d runs, want exactly one more", runs.Load())
	}
	if maxRunning.Load() != 1 {
		t.Fatalf("submissions overlapped (%d at once)", maxRunning.Load())
	}
}

func TestExpiryAndAccountingRunInSeparateLoops(t *testing.T) {
	// The expiry ticker must not wait for a slow accounting pass: run both on one period with the first blocked.
	var slow, fast atomic.Int32
	block := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := func() time.Duration { return 2 * time.Millisecond }
	go ticker(ctx, p, func(time.Time) { slow.Add(1); <-block })
	go ticker(ctx, p, func(time.Time) { fast.Add(1) })
	deadline := time.Now().Add(2 * time.Second)
	for fast.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fast.Load() < 5 || slow.Load() != 1 {
		t.Fatalf("fast=%d slow=%d: a blocked loop held up the other", fast.Load(), slow.Load())
	}
	close(block)
}

func TestAStalledSubmitterShowsOnHealth(t *testing.T) {
	p := &phase3{}
	p.lastSubmitOK = time.Now().Add(-planValidity - time.Second)
	if got := p.degradedSummary(); got != reasonSubmitterStalled {
		t.Fatalf("summary = %q", got)
	}
	p.lastSubmitOK = time.Now()
	if got := p.degradedSummary(); got != "" {
		t.Fatalf("a submitter that just delivered a plan reported %q", got)
	}
}

func TestHealthStringsAreSafeAcrossLoops(t *testing.T) {
	p := &phase3{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				p.setDegraded("x")
				p.setExpiryDegraded("y")
				_ = p.degradedSummary()
				_ = p.Degraded()
			}
		}()
	}
	wg.Wait()
}
