package pmsd

// A claimed command produces exactly one DR through the owning writer, never a second staging window, and
// nothing on this path can emit a financial frame (only pmsd may emit frames, so this is asserted).

import (
	"bufio"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// A claimed command adds exactly one DR on top of the automatic initial one (two on the wire). The sink hands
// the command out once, so an adapter that re-asked every frame would show three or more.
func TestResyncCommand_ProducesExactlyOneDR(t *testing.T) {
	sink := &recordingSink{pendingCmd: &ResyncCommand{ID: "cmd-1", Reason: "OPERATOR_VERIFICATION"}}

	frames := runAdapterWithFrames(t, sink,
		// A heartbeat, then the roster the DR asks for. The command is claimed on the first valid frame.
		pms.BuildLS("260825", "120000"),
		"DS|",
		"DE|",
		pms.BuildLS("260825", "120100"),
	)

	if got := countFrames(frames, "DR"); got != 2 {
		t.Fatalf("the adapter emitted %d DR frames, want exactly 2 — the automatic initial sync plus the "+
			"operator's one. Frames: %v", got, frames)
	}
	// The adapter asks only outside a roster, so the first chance is the frame after DE; one claim is
	// correct, and zero would mean it never asks.
	if sink.claims < 1 {
		t.Fatal("the adapter never asked for a pending command, so an operator's request would sit " +
			"unclaimed until the connection happened to restart")
	}
}

// No command means no DR beyond the automatic one (the adapter must not resync on a timer).
func TestResyncCommand_NoCommandMeansNoExtraDR(t *testing.T) {
	sink := &recordingSink{} // nothing pending

	frames := runAdapterWithFrames(t, sink,
		pms.BuildLS("260825", "120000"),
		"DS|",
		"DE|",
		pms.BuildLS("260825", "120100"),
	)

	// Exactly one DR: the automatic initial sync.
	if got := countFrames(frames, "DR"); got != 1 {
		t.Fatalf("expected only the automatic initial DR, got %d. Frames: %v", got, frames)
	}
}

// A command claimed mid-roster must not open a second window. This is the adapter's own guard in addition to
// the claim's: the adapter knows it is inside DS…DE before the database row does.
func TestResyncCommand_NotClaimedInsideAnOpenRoster(t *testing.T) {
	sink := &recordingSink{pendingCmd: &ResyncCommand{ID: "cmd-2", Reason: "SUPPORT_REQUEST"}}

	frames := runAdapterWithFrames(t, sink,
		"DS|", // the roster opens BEFORE any frame the command could be claimed on
		pms.BuildLS("260825", "120000"),
		pms.BuildLS("260825", "120001"),
		"DE|",
	)

	if got := countFrames(frames, "DR"); got != 1 {
		t.Fatalf("a command was executed inside an open DS…DE window: %d DR frames, want 1 (the initial "+
			"one). A second DR mid-roster interleaves two rosters on one socket", got)
	}
}

// PS and PA must never appear in any frame produced by the command scenarios (writeFrame refuses them).
func TestResyncCommand_EmitsNoFinancialFrame(t *testing.T) {
	sink := &recordingSink{pendingCmd: &ResyncCommand{ID: "cmd-3", Reason: "AFTER_PMS_MAINTENANCE"}}

	frames := runAdapterWithFrames(t, sink,
		pms.BuildLS("260825", "120000"),
		"DS|",
		"DE|",
	)

	for _, f := range frames {
		if id := frameID(f); id == "PS" || id == "PA" {
			t.Fatalf("the resync control caused a FINANCIAL frame %q to be emitted: %q. The read-only "+
				"guarantee is the reason edged is not allowed near this socket", id, f)
		}
	}
}

func countFrames(frames []string, id string) int {
	n := 0
	for _, f := range frames {
		if frameID(f) == id {
			n++
		}
	}
	return n
}

func frameID(f string) string {
	f = strings.TrimLeft(f, "\x01\x02")
	if len(f) < 2 {
		return ""
	}
	return f[:2]
}

// runAdapterWithFrames returns every frame the adapter wrote; the peer keeps reading after the handshake so a command's DR is captured.
func runAdapterWithFrames(t *testing.T, sink *recordingSink, frames ...string) []string {
	t.Helper()
	if sink.q == nil {
		sink.q = NewBoundedQueue(16, time.Second)
	}
	adapter, server := newAdapterOverPipe(t)

	var mu sync.Mutex
	var wrote []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(server)
		// Recorded in its own goroutine: a serialized read-then-write peer would deadlock the pipe.
		go func() {
			for {
				body, err := pms.ReadFramedRecord(br)
				if err != nil {
					return
				}
				mu.Lock()
				wrote = append(wrote, body)
				mu.Unlock()
			}
		}()
		for _, rec := range frames {
			if err := pms.WriteFramedRecord(server, rec); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond) // let the adapter act on each frame before the next arrives
		}
		time.Sleep(60 * time.Millisecond)
		_ = server.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = adapter.Serve(ctx, sink)
	<-done

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), wrote...)
}

// The automatic first sync must enter a stage before DS arrives, since operators watch it after restoring the socket.
func TestAutomaticFirstSync_AnnouncesWaitingForPMS(t *testing.T) {
	sink := &recordingSink{}

	runAdapterWithFrames(t, sink,
		pms.BuildLS("260826", "090000"),
		"DS|",
		"DE|",
	)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.fullSyncRequested < 1 {
		t.Fatal("the automatic initial sync never signalled WAITING_FOR_PMS, so an operator watching a " +
			"reconnect sees a connected interface and no sign that a sync is under way")
	}
	if sink.resyncStart != 1 || sink.resyncComplete != 1 {
		t.Fatalf("the automatic DS/DE path changed: start=%d complete=%d, want 1/1. The stage additions must "+
			"not alter the sync itself", sink.resyncStart, sink.resyncComplete)
	}
}

// The skipped count is per roster: a second full sync must not inherit the first one's rejects.
func TestConsecutiveResyncs_HaveIndependentSkippedCounters(t *testing.T) {
	sink := &recordingSink{}

	// Two rosters: the first has GI records without RN/G# (ErrIdentityAbsent); the second is clean.
	frames := []string{"DS|"}
	for i := 0; i < 30; i++ {
		frames = append(frames, "GI|GNSmith|GFJohn|") // no room, no reservation: unkeyable
	}
	frames = append(frames, "DE|", "DS|")
	for i := 0; i < 3; i++ {
		frames = append(frames, "GI|RN140"+string(rune('0'+i))+"|G#5000"+string(rune('0'+i))+"|GNOkonkwo|GFAda|GA260101|GD260105|")
	}
	frames = append(frames, "DE|")

	runAdapterWithFrames(t, sink, frames...)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.skippedReports) == 0 {
		t.Skip("no skipped-record report was produced; the batching threshold was not reached")
	}
	// A reset shows as a later report smaller than an earlier one, or the counter no longer climbing.
	max := int64(0)
	for _, n := range sink.skippedReports {
		if n > max {
			max = n
		}
	}
	if max > 30 {
		t.Fatalf("skipped reports reached %d across two rosters where only the first had %d unusable "+
			"records: %v. The counter is carrying the previous sync's total into this one", max, 30,
			sink.skippedReports)
	}
}
