// Command phase3-foundation installs, inspects or removes the Phase-3 packet-authorization foundation in the live nftables ruleset without disturbing current guests.
// It is an operator tool, not daemon startup; it authorizes nobody, and each mode prints a JSON report that is the legacy-parity evidence.
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

	// The report is printed even on failure so the operator knows the state of a live ruleset.
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
