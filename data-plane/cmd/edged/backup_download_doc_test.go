package main

import "testing"

// Holds the documented backups and backup-download rows to the code cell by cell.
func TestBackupResourcesMatchTheDocumentedRoleMatrix(t *testing.T) {
	for _, resource := range []string{"backups", "backup-download"} {
		row := docMatrixRow(t, resource)
		for role := range rolePerms {
			if role == "tenant_admin" || role == "tenant_operator" {
				continue
			}
			if _, ok := row[role]; !ok {
				t.Errorf("the documented %s row says nothing about %s", resource, role)
			}
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
				if !read || write {
					t.Errorf("%s is documented R on %s but the code grants read=%v write=%v", role, resource, read, write)
				}
			case "–", "-", "—":
				if read || write {
					t.Errorf("%s is documented as having no access to %s but the code grants read=%v write=%v",
						role, resource, read, write)
				}
			default:
				t.Errorf("unrecognised permission cell %q for %s on %s", cell, role, resource)
			}
		}
	}
}
