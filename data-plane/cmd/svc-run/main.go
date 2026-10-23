// svc-run is an exec wrapper giving non-Go appliance services the same adaptive crash-loop backoff as the Go daemons.
//
// Usage in a unit's ExecStart:
//
//	ExecStart=/opt/stayconnect/bin/svc-run <health-name> -- /usr/bin/real args...
//
// It records the start in the shared backoff tracker (sleeping with jittered exponential backoff if crash-looping), then execs the real command in
// place (same PID, so systemd supervision is unchanged).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: svc-run <health-name> [-- <command> [args...]]")
		os.Exit(2)
	}
	name := args[0]

	// Without --, backoff only (ExecStartPre= on units whose ExecStart is not rewritten, e.g. distro Kea/Unbound); with -- <cmd...>, backoff then exec.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}

	startupbackoff.Guard(name)

	if sep < 0 {
		return
	}
	if sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "svc-run: nothing after --")
		os.Exit(2)
	}
	cmd := args[sep+1:]

	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "svc-run: cannot find", cmd[0], ":", err)
		os.Exit(127)
	}
	// Exec in place so the real service keeps this PID under systemd.
	if err := syscall.Exec(bin, cmd, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "svc-run: exec failed:", err)
		os.Exit(126)
	}
}
