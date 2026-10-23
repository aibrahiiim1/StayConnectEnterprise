//go:build integration

package pmsd

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// gatepIamV2TableGrants replays deploy/gatep/gatep-grants.sql (following `\ir` as psql does) from an empty set,
// since its preamble revokes everything first. Column-level grants are skipped: role_table_grants
// does not report them. Keys are "grantee|table|PRIVILEGE".
func gatepIamV2TableGrants(t *testing.T) map[string]bool {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	dir := filepath.Join(filepath.Dir(self), "..", "..", "..", "deploy", "gatep")
	var text strings.Builder
	var load func(name string, depth int)
	load = func(name string, depth int) {
		if depth > 4 {
			t.Fatalf("gatep include depth exceeded at %s", name)
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read gatep %s: %v", name, err)
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, `\ir `) || strings.HasPrefix(trim, `\i `) {
				f := strings.Fields(trim)
				load(f[len(f)-1], depth+1)
				continue
			}
			if strings.HasPrefix(trim, `\`) {
				continue
			}
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			text.WriteString(line)
			text.WriteString("\n")
		}
	}
	load("gatep-grants.sql", 0)

	stmtRe := regexp.MustCompile(`(?is)^\s*(GRANT|REVOKE)\s+(.+?)\s+ON\s+(?:TABLE\s+)?(.+?)\s+(?:TO|FROM)\s+(.+?)\s*$`)
	allPrivs := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
	set := map[string]bool{}
	for _, stmt := range strings.Split(text.String(), ";") {
		m := stmtRe.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		verb, privs, objs, roles := strings.ToUpper(m[1]), m[2], m[3], m[4]
		upObjs := strings.ToUpper(objs)
		if strings.Contains(upObjs, "FUNCTION") || strings.Contains(upObjs, "SEQUENCE") ||
			strings.Contains(upObjs, "SCHEMA") || strings.Contains(upObjs, "DATABASE") {
			continue
		}
		var tables []string
		for _, o := range strings.Split(objs, ",") {
			o = strings.TrimSpace(o)
			if strings.HasPrefix(o, "iam_v2.") {
				tables = append(tables, strings.TrimPrefix(o, "iam_v2."))
			}
		}
		var grantees []string
		for _, r := range strings.Split(strings.TrimSuffix(strings.TrimSpace(roles), " CASCADE"), ",") {
			r = strings.TrimSpace(r)
			if strings.HasPrefix(r, "svc_") {
				grantees = append(grantees, r)
			}
		}
		if len(tables) == 0 || len(grantees) == 0 {
			continue
		}
		var ps []string
		depth, start := 0, 0
		split := func(end int) {
			p := strings.TrimSpace(privs[start:end])
			if p != "" && !strings.Contains(p, "(") { // a column grant is not a table grant
				up := strings.ToUpper(p)
				if up == "ALL" || up == "ALL PRIVILEGES" {
					ps = append(ps, allPrivs...)
				} else {
					ps = append(ps, up)
				}
			}
		}
		for i, c := range privs {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					split(i)
					start = i + 1
				}
			}
		}
		split(len(privs))
		for _, g := range grantees {
			for _, tb := range tables {
				for _, p := range ps {
					k := g + "|" + tb + "|" + p
					if verb == "GRANT" {
						set[k] = true
					} else {
						delete(set, k)
					}
				}
			}
		}
	}
	if len(set) == 0 {
		t.Fatal("parsed no iam_v2 table grants from deploy/gatep — the derivation is broken, not the database")
	}
	return set
}
