package main

import "testing"

// AD-9: only an encrypted database dump can leave the appliance; everything else is listed, not downloadable.
func TestDownloadPolicyOnlyEncryptedDatabaseDumps(t *testing.T) {
	cases := []struct {
		name string
		dir  bool
		ok   bool
	}{
		{"db-20261028T000000Z.sql.gz.age", false, true},
		{"db-safety-20261028T000000Z.sql.gz.age", false, true},
		{"db-20261028T000000Z.sql.gz", false, false}, // unencrypted: refused
		{"deploy-1.tgz", false, false},
		{"gatep-20261028.sql", false, false},
		{"db-notes.age", false, false},
		{"db-20261028T000000Z.sql.gz.age", true, false},
	}
	for _, c := range cases {
		ok, _, why := downloadPolicy(c.name, c.dir)
		if ok != c.ok {
			t.Errorf("%s dir=%v: downloadable=%v want %v", c.name, c.dir, ok, c.ok)
		}
		if !ok && why == "" {
			t.Errorf("%s: a refusal needs an explanation", c.name)
		}
	}
}
