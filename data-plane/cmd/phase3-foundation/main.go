// Command phase3-foundation installs, inspects or removes the Phase-3 packet-authorization foundation in the
// LIVE nftables ruleset, without disturbing the guests currently on it.
//
// It is an OPERATOR tool, run once during an authorized live-dark deployment and once more if that deployment
// is rolled back. It is deliberately not part of any daemon's startup: installing it is a decision with a
// before-and-after that somebody has to record, and a daemon that did it silently on boot would make the
// legacy-parity proof impossible to produce.
//
// It authorizes nobody, enables no feature flag, and reads no guest data. The set it creates is empty, and an
// empty set matches nothing — so an appliance that has run `install` while Phase 3 is dark forwards exactly
// what it forwarded before.
//
// Usage:
//
//	phase3-foundation inspect     # read-only: what is present, and who is currently authorized
//	phase3-foundation install     # surgical, idempotent, verified, self-rolling-back on failure
//	phase3-foundation rollback    # removes ONLY the Phase-3 foundation
//
// Every mode prints a JSON report on stdout, including the legacy authorization elements before and after the
// mutation. That output IS the legacy-parity evidence the runbook asks the operator to keep.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nftfoundation"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: phase3-foundation inspect|install|rollback")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := nftfoundation.New()
	if p := os.Getenv("NFT_PATH"); p != "" {
		f.NftPath = p
	}

	var out any
	var err error
	switch os.Args[1] {
	case "inspect":
		out, err = f.Inspect(ctx)
	case "install":
		out, err = f.Install(ctx)
	case "rollback":
		out, err = f.Rollback(ctx)
	default:
		fmt.Fprintln(os.Stderr, "usage: phase3-foundation inspect|install|rollback")
		os.Exit(2)
	}

	// The report is printed even on failure: a failed install that rolled itself back still has to say what it
	// found and what it did, or the operator is left guessing at the state of a live ruleset.
	if out != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phase3-foundation:", err)
		os.Exit(1)
	}
}
