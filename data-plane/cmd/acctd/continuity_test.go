package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
)

type valRow struct {
	v   int
	err error
}

func (r valRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int)) = r.v
	return nil
}

type valDB struct{ row valRow }

func (d valDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { return d.row }

func TestReadContinuityMinutes_ClampsAndToleratesAnOlderSchema(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 60: 60, 120: 120, 500: 120} {
		got, found, err := readContinuityMinutes(context.Background(), valDB{valRow{v: in}})
		if err != nil || !found || got != want {
			t.Errorf("%d -> %d found=%v err=%v, want %d", in, got, found, err, want)
		}
	}
	// 42703 undefined_column: the migration is not applied; the answer is "not found", never a guess.
	_, found, err := readContinuityMinutes(context.Background(), valDB{valRow{err: &pgconn.PgError{Code: "42703"}}})
	if err != nil || found {
		t.Fatalf("older schema: found=%v err=%v", found, err)
	}
	_, _, err = readContinuityMinutes(context.Background(), valDB{valRow{err: errors.New("connection refused")}})
	if err == nil {
		t.Fatal("a database error must be reported so the value in force is kept")
	}
}

// The producer says nothing until it has read the setting; once it has, 0 is stated explicitly so netd drops what it holds.
func TestContinuityFor_ZeroIsStatedOnlyOnceTheSettingIsKnown(t *testing.T) {
	p := &phase3{enf: enforce.New(nil)}
	if p.continuityFor(context.Background(), enforce.Plan{}, time.Now()) != nil {
		t.Fatal("an unread setting must produce no statement at all")
	}
	p.contKnown.Store(true)
	blk := p.continuityFor(context.Background(), enforce.Plan{}, time.Now())
	if blk == nil || !blk.IsOff() {
		t.Fatalf("a known 0 must be an explicit off: %+v", blk)
	}
}

// A stalled accounting loop stops the producer vouching for budgets.
func TestContinuityFor_NoBlockWhileAccountingIsStale(t *testing.T) {
	p := &phase3{enf: enforce.New(nil)}
	p.contKnown.Store(true)
	p.contWindow.Store(3600)
	now := time.Now()
	p.lastPassOK = now.Add(-5 * time.Minute)
	if p.continuityFor(context.Background(), enforce.Plan{}, now) != nil {
		t.Fatal("a block was sent while accounting was five minutes stale")
	}
	p.lastPassOK = now
	p.acctDegraded = "counters unreadable"
	if p.continuityFor(context.Background(), enforce.Plan{}, now) != nil {
		t.Fatal("a block was sent while accounting was degraded")
	}
}
