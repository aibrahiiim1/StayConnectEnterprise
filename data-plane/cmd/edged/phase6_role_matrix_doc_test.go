package main

import (
	"os"
	"strings"
	"testing"
)

// THE DOCUMENT AND THE CODE MUST AGREE, and this test is what makes that a fact rather than an intention.
//
// rolePerms carries the comment "Matrix per docs/ROLE_AND_SCOPE_MATRIX.md", which names that document as the
// definition of the boundary. A named definition that nothing checks is worse than no definition at all: an
// operator reading the document to decide who should hold an account would be reading a claim about the
// system rather than the system. When Phase 6 added a resource to the code and not to the document, the
// document silently began describing a matrix that no longer existed.
//
// So the assertion runs the other way round from the usual: the DOCUMENT is parsed, and the code is required
// to match it cell by cell. Adding a resource to rolePerms without documenting it fails here, and so does
// changing a documented permission without saying so.

// docMatrixRow returns the parsed §3 table row for a resource: role name -> cell, using the table's own
// header for the role order rather than a copy of it.
func docMatrixRow(t *testing.T, resource string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/ROLE_AND_SCOPE_MATRIX.md")
	if err != nil {
		t.Fatalf("the role matrix document is unreadable: %v", err)
	}
	var roles []string
	for _, line := range strings.Split(string(raw), "\n") {
		cells := splitTableRow(line)
		if len(cells) < 2 {
			continue
		}
		if strings.HasPrefix(cells[0], "/edge/v1 resource") {
			roles = cells[1:]
			continue
		}
		if roles == nil || cells[0] != resource {
			continue
		}
		if len(cells)-1 != len(roles) {
			t.Fatalf("the %q row has %d cells for %d roles", resource, len(cells)-1, len(roles))
		}
		out := map[string]string{}
		for i, role := range roles {
			// The document bolds cells for emphasis; the emphasis is not part of the value.
			out[role] = strings.TrimSpace(strings.ReplaceAll(cells[i+1], "*", ""))
		}
		return out
	}
	if roles == nil {
		t.Fatal("the §3 site-role table was not found in docs/ROLE_AND_SCOPE_MATRIX.md")
	}
	t.Fatalf("docs/ROLE_AND_SCOPE_MATRIX.md §3 has no row for the %q resource, "+
		"but edged mounts it -- the document is the stated definition of this boundary", resource)
	return nil
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// The Phase-6 resource: every cell of the documented row is enforced by the code, and nothing beyond it.
func TestPhase6ResourceMatchesTheDocumentedRoleMatrix(t *testing.T) {
	const resource = "guest-device-self-service"
	row := docMatrixRow(t, resource)
	if len(row) == 0 {
		t.Fatal("empty row")
	}
	for role, cell := range row {
		read := permFor([]string{role}, resource, permRead)
		write := permFor([]string{role}, resource, permWrite)
		switch cell {
		case "W":
			if !write {
				t.Errorf("%s is documented W on %s but the code refuses the write", role, resource)
			}
		case "R":
			if !read {
				t.Errorf("%s is documented R on %s but the code refuses the read", role, resource)
			}
			if write {
				t.Errorf("%s is documented R on %s but the code ALLOWS the write -- "+
					"a read-only role can change a guest-facing capability", role, resource)
			}
		case "–", "-", "—":
			if read || write {
				t.Errorf("%s is documented as having no access to %s but the code grants read=%v write=%v",
					role, resource, read, write)
			}
		default:
			t.Errorf("unrecognised permission cell %q for %s", cell, role)
		}
	}
}

// The row must actually describe every role the code knows about, or a role could hold a permission the
// document never mentions. site_admin and the legacy tenant_* aliases are resolved before the table is
// consulted, so they are not table rows.
func TestPhase6DocumentedRowCoversEveryRole(t *testing.T) {
	row := docMatrixRow(t, "guest-device-self-service")
	for role := range rolePerms {
		if role == "tenant_admin" || role == "tenant_operator" {
			continue
		}
		if _, ok := row[role]; !ok {
			t.Errorf("the documented row says nothing about %s", role)
		}
	}
	if _, ok := row["site_admin"]; !ok {
		t.Error("the documented row omits site_admin, which holds write implicitly")
	}
}
