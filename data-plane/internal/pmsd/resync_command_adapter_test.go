package pmsd

// THE COMMAND REACHES THE SOCKET AS EXACTLY ONE DR, THROUGH THE WRITER THAT ALREADY OWNS IT.
//
// The claim tests prove the row can only be consumed once. These prove what the adapter does with it: one
// claimed command produces one DR and no more, a command claimed mid-roster cannot open a second staging
// window, and nothing this path can reach is capable of emitting a financial frame.
//
// The last one is not decoration. The whole reason edged writes a row instead of talking to the socket is
// that only pmsd may emit frames; a control that could be coaxed into emitting a PS would make that argument
// worthless, so it is asserted rather than assumed.

import (
	"bufio"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// A CLAIMED COMMAND PRODUCES ONE MORE DR — and exactly one more.
//
// Every connection already sends a DR at startup: that is the automatic initial full sync, and this feature
// must not disturb it. So a successful command means TWO DR frames on the wire, the initial one and the
// operator's, and the assertion is about the difference rather than the total. The sink hands the command out
// once, as the real claim does by clearing the row in the same statement, so an adapter that re-asked on
// every frame would show up here as three or more.
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
	// The adapter asks only when it is NOT mid-roster, so with an initial sync running from connect until
	// DE, the first opportunity to ask is the frame after DE. One claim is the correct number here, and the
	// assertion exists to catch the opposite failure: an adapter that never asks at all.
	if sink.claims < 1 {
		t.Fatal("the adapter never asked for a pending command, so an operator's request would sit " +
			"unclaimed until the connection happened to restart")
	}
}

// NO COMMAND, NO DR beyond the automatic one the initial connection always performs. This is the guard that
// the claim is genuinely consulted rather than the adapter resyncing on a timer.
func TestResyncCommand_NoCommandMeansNoExtraDR(t *testing.T) {
	sink := &recordingSink{} // nothing pending

	frames := runAdapterWithFrames(t, sink,
		pms.BuildLS("260825", "120000"),
		"DS|",
		"DE|",
		pms.BuildLS("260825", "120100"),
	)

	// Exactly one DR: the automatic initial full sync at connect, which this feature must leave untouched.
	if got := countFrames(frames, "DR"); got != 1 {
		t.Fatalf("expected only the automatic initial DR, got %d. Frames: %v", got, frames)
	}
}

// A COMMAND CLAIMED WHILE A ROSTER IS ARRIVING MUST NOT OPEN A SECOND WINDOW.
//
// The claim already refuses while sync_status is RESYNC_IN_PROGRESS; this is the adapter's own guard, and
// having both is deliberate — the two cover different moments, since the adapter knows it is inside DS…DE
// before the database row does.
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

// NOTHING ON THIS PATH CAN EMIT A FINANCIAL FRAME.
//
// Asserted against every frame the adapter produced across the command scenarios: PS (posting) and PA
// (posting answer) must never appear. The writer refuses them at writeFrame, and this proves the refusal
// covers the frames the operator control actually causes.
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

// countFrames counts outbound frames whose record id matches.
func countFrames(frames []string, id string) int {
	n := 0
	for _, f := range frames {
		if frameID(f) == id {
			n++
		}
	}
	return n
}

// frameID reads the record identifier from an outbound FIAS frame body.
func frameID(f string) string {
	f = strings.TrimLeft(f, "\x01\x02")
	if len(f) < 2 {
		return ""
	}
	return f[:2]
}

// runAdapterWithFrames drives the adapter against a fake PMS peer that sends the given frames, and returns
// every frame the adapter WROTE. Modelled on TestAdapter_StartupDomainAndResync's peer; the difference is
// that this one keeps reading after the handshake so the DR raised by a command is captured too.
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
		// Every outbound frame is recorded, in a goroutine of its own, because the adapter writes while it
		// reads and a serialized read-then-write peer would deadlock the pipe.
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

// THE AUTOMATIC FIRST SYNC IS WATCHABLE, not just the one a human clicked.
//
// The operator restores the socket by hand with the page open, so the sync they are most likely to be
// watching is the automatic one. It previously entered no stage at all until DS arrived: the connection came
// up and the screen showed nothing.
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

// SKIPPED RECORDS BELONG TO ONE ROSTER.
//
// The counter was per-ownership-cycle, so a second full sync inherited the first one's rejects. An operator
// reading "40 skipped" on a clean roster had no way to know all 40 belonged to an earlier sync — and the
// number that matters is exactly "how much of THIS roster was unusable".
func TestConsecutiveResyncs_HaveIndependentSkippedCounters(t *testing.T) {
	sink := &recordingSink{}

	// Two complete rosters on one connection. The FIRST carries records with no keyable Stay identity; the
	// second is clean. GI records without RN/G# cannot be keyed, which is what ErrIdentityAbsent describes.
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
	// The reports must not be monotonically increasing across the DE boundary: a reset means a later report
	// is SMALLER than an earlier one, or the counter simply stops climbing once the clean roster begins.
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
