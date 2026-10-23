//go:build integration

package main

import (
	"bufio"
	"context"
	"os"
	"sort"
	"strings"
	"testing"
)

// Tripwire: testdata/personal_column_candidates.txt lists every column whose name suggests a person, each already
// handled by erasure or retention, kept by law, staff configuration, or not about a person. A column not in the list
// is new and fails this test until someone decides what erasure does with it (erasure, scrub registry, export,
// docs/DATA_OWNERSHIP.md section 9) and adds it. It cannot tell a column is handled, only that it was looked at.
const personalColumnQuery = `
select table_schema||'.'||table_name||'.'||column_name from information_schema.columns c
 where table_schema in ('public','iam_v2')
   and column_name ~* '(email|phone|msisdn|first_name|last_name|full_name|guest_name|display_name|^name$|mac|ip_addr|^ip$|client_ip|remote_ip|user_agent|room|passport|address|username|login|national|birth|reservation|g_number|external_guest|note|comment|reason|label|destination|recipient|contact|identity|^actor)'
   and table_name not in (select table_name from information_schema.views where table_schema in ('public','iam_v2'))
   and table_name !~ '^(_|pg_)' and table_name !~ '_[0-9]+_chunk$'
 order by 1`

func TestIntegration_PersonalColumnTripwire_NoUnconsideredColumn(t *testing.T) {
	f := newAPI(t)
	rows, err := f.pool.Query(context.Background(), personalColumnQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		have[c] = true
	}
	fh, err := os.Open("testdata/personal_column_candidates.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	known := map[string]bool{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			known[l] = true
		}
	}
	var added, gone []string
	for c := range have {
		if !known[c] {
			added = append(added, c)
		}
	}
	for c := range known {
		if !have[c] {
			gone = append(gone, c)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	if len(added) > 0 {
		t.Errorf("columns that look like personal data and have not been considered (decide what an erasure does with each, "+
			"see this test's comment, then add them to testdata/personal_column_candidates.txt):\n  %s", strings.Join(added, "\n  "))
	}
	if len(gone) > 0 {
		t.Errorf("columns in testdata/personal_column_candidates.txt that no longer exist (remove them):\n  %s", strings.Join(gone, "\n  "))
	}
}
