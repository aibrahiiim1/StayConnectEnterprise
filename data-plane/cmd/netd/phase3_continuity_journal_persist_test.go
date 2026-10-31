package main

// A revocation entry a later database read has absorbed is pruned from memory. It must be pruned from the file too: entries match
// by ADDRESS, so a stale entry reloaded by the next netd start would be applied to whoever holds that address by then. Seen in the
// lab: five healthy guests were cut at a netd restart during an outage by staff disconnects made hours earlier on the same addresses.

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func TestContinuityJournal_APrunedEntryIsNotReloadedByTheNextStart(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	ss := []shapeplan.Session{a}
	mk := func() *shapeplan.Continuity { return block(time.Hour, ss, nil, nil) }
	h.healthy(10, mk, ss...)
	s := &server{phase3: h.p}
	if rec := postRevoke(t, s, `{"session_ids":["c-1"],"reason":"STAFF_DISCONNECT"}`); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(h.p.cont.journalPath)
	if err != nil || !strings.Contains(string(raw), "STAFF_DISCONNECT") {
		t.Fatalf("the revocation must be journalled first: %v %s", err, raw)
	}
	// Healthy plans whose block was read after the revocation: the entry has done its job and leaves memory...
	h.healthy(30, mk, ss...)
	if st := h.p.contStatus(); st.Revocations != 0 {
		t.Fatalf("the absorbed entry should be pruned from memory: %+v", st)
	}
	// ...and the file.
	raw, err = os.ReadFile(h.p.cont.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "STAFF_DISCONNECT") {
		t.Fatalf("the pruned entry is still on disk, so the next start would apply it to the new holder of the address: %s", raw)
	}
}
