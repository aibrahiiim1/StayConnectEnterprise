package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAsideExpired(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		days int
		want bool
	}{
		{pgDatabase + "_before_20260901t090000z", 14, true},
		{pgDatabase + "_before_20261015t090000z", 14, false},
		{pgDatabase + "_before_20260901t090000z", 90, false},
		{pgDatabase, 14, false},
		{pgDatabase + "_before_garbage", 14, false},
		{"other_before_20200101t000000z", 14, false},
		{pgDatabase + "_before_20260901t090000z", 0, false},
	}
	for _, c := range cases {
		if got := asideExpired(c.name, now, c.days); got != c.want {
			t.Errorf("asideExpired(%q, %d) = %v, want %v", c.name, c.days, got, c.want)
		}
	}
}

func TestSweepPartials_OnlyOldPartialFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "a.sql.gz.partial")
	fresh := filepath.Join(dir, "b.sql.gz.partial")
	real := filepath.Join(dir, "c.sql.gz")
	for _, f := range []string{old, fresh, real} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(real, past, past)
	if n := sweepPartials(dir, time.Now(), 24*time.Hour); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the old partial file is still there")
	}
	for _, f := range []string{fresh, real} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was removed", f)
		}
	}
}
