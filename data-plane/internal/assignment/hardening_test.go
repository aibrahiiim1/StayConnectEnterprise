package assignment_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

// P2 L1: an older signed document written back to assignment.json is not accepted once a newer one was adopted.
func TestResolveRefusesDocumentOlderThanAdopted(t *testing.T) {
	f := assignmenttest.New(t)
	f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
	f.WriteAssignment(t, f.Doc(assignment.StateRevoked, 2))
	// An old copy (version 1, granting) is put back on disk.
	f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
	r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil)
	if r.Outcome != assignment.OutcomeUnverifiable || r.TenantID != "" {
		t.Fatalf("an older document must be refused, got %+v", r)
	}
	// A factory reset (Clear) forgets the high-water mark with the record.
	if err := (&assignment.Store{Dir: f.Paths.Dir}).Clear(); err != nil {
		t.Fatal(err)
	}
	f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
	if r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil); r.Outcome != assignment.OutcomeGranted {
		t.Fatalf("after a reset version 1 is the newest again, got %+v", r)
	}
}

// P2 L18: refresh notes never overwrite a concurrent adoption.
func TestNoteRefreshDoesNotOverwriteAdoption(t *testing.T) {
	f := assignmenttest.New(t)
	store := &assignment.Store{Dir: f.Paths.Dir}
	f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
	const n = 40
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					store.NoteRefresh(true)
				}
			}
		}()
	}
	for v := int64(2); v <= n; v++ {
		if err := store.Adopt(f.Doc(assignment.StateAssigned, v)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	rec, err := store.Load()
	if err != nil || rec.Current == nil || rec.Current.Version != n {
		t.Fatalf("latest adoption lost: %+v %v", rec, err)
	}
}
