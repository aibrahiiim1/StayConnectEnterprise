//go:build linux

package main

// WHO IS ON THE OTHER END OF THE SOCKET, AND WHY IT HAD TO BE ASKED.
//
// scd's admin surface is a unix socket at /run/stayconnect/scd.sock, mode 0660, owned root:stayconnect.
// Its trust model was "edged has already authenticated the operator, checked the resource permission and
// re-verified the password; this socket is not reachable from the network". That model was written for
// license installs and backup runs, and it has a hole that only became serious when this delivery put
// PLAINTEXT GUEST CREDENTIALS behind the same socket:
//
//	edged   runs as stayconnect
//	portald runs as stayconnect      <- the NETWORK-FACING captive portal
//
// Verified on the appliance, not assumed: `srw-rw---- root stayconnect /run/stayconnect/scd.sock`, and
// systemd reports User=stayconnect for both services. So portald can POST to /v1/vouchers/{id}/reveal with
// any real operator UUID and a reason of its choosing, and receive the code -- no step-up, no permission
// check, because those happen in edged and nothing proves they happened.
//
// WHAT THIS CHECK IS, AND WHAT IT HONESTLY IS NOT.
//
// SO_PEERCRED gives the peer's uid, gid and pid. The uid cannot separate edged from portald, because they
// share it. What can be read is the peer's own executable: /proc/<pid>/exe resolves, in the kernel, to the
// real path of the running image. Requiring it to be the installed edged binary -- root-owned and not
// writable by stayconnect -- means portald cannot reach these routes AS ITSELF, and cannot reach them by
// exec'ing a copy it placed somewhere it can write.
//
// IT IS NOT A BOUNDARY AGAINST A COMPROMISED PEER, and pretending otherwise would be worse than not having
// it. A process running as stayconnect can ptrace edged unless the kernel forbids it, so under a shared uid
// a full compromise of portald reaches edged's memory whatever this function does. THE REAL FIX IS TO STOP
// SHARING THE UID: portald gets its own user, and the socket group admits edged only. That is a deployment
// change -- new systemd User=, a new group, and verification that the guest portal still works -- and it is
// recorded as a required hardening item rather than done halfway here.
//
// So this is defence in depth with its depth stated: it stops an honest mistake, a future service added to
// the stayconnect group, and a buggy-but-not-compromised portald. It does not stop an attacker who already
// owns the uid.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

type peerKey struct{}

// peerCred is what the kernel said about the other end.
type peerCred struct {
	PID  int32
	UID  uint32
	Exe  string // resolved /proc/<pid>/exe, or "" when it could not be read
	Read bool   // whether the credentials were obtained at all
}

// withPeerCred is the http.Server ConnContext hook: it reads the peer credentials once, at accept time,
// and puts them in the connection's context so every handler on that connection can ask.
//
// AT ACCEPT TIME, not per request, and that ordering matters: by the time a handler runs, the peer may
// have exited and /proc/<pid>/exe would be gone -- which would turn a legitimate caller into a refusal.
func withPeerCred(ctx context.Context, c net.Conn) context.Context {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *syscall.Ucred
	var cerr error
	_ = raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cerr != nil || cred == nil {
		return ctx
	}
	p := peerCred{PID: cred.Pid, UID: cred.Uid, Read: true}
	if exe, lerr := os.Readlink(fmt.Sprintf("/proc/%d/exe", cred.Pid)); lerr == nil {
		p.Exe = exe
	}
	return context.WithValue(ctx, peerKey{}, p)
}

// peerFrom returns what was learned at accept time.
func peerFrom(ctx context.Context) (peerCred, bool) {
	p, ok := ctx.Value(peerKey{}).(peerCred)
	return p, ok
}

// edgedBinaryPaths are the images allowed to call the code-recovery routes.
//
// The deployed layout is a release directory with a symlink, so the resolved path is the versioned one;
// both spellings are accepted, and SCD_EDGED_BINARY overrides for a test or an unusual install. A prefix
// match under the release root is deliberately NOT used: "anything under /opt/stayconnect/releases" would
// admit any binary a future packaging step drops there.
func edgedBinaryPaths() []string {
	if v := os.Getenv("SCD_EDGED_BINARY"); v != "" {
		return []string{v}
	}
	// VERIFIED ON THE APPLIANCE, not guessed: systemd's ExecStart is /opt/stayconnect/bin/edged and the
	// running process's /proc/<pid>/exe resolves to the same path. Getting this list wrong fails CLOSED,
	// which would make the reveal stop working rather than become unsafe -- but a security check that
	// breaks the feature is still a defect, so the path was read from the machine.
	return []string{
		"/opt/stayconnect/bin/edged",
		"/usr/local/bin/edged",
	}
}

// requireEdgedPeer refuses a request that did not come from edged.
//
// FAIL CLOSED WHEN THE PEER CANNOT BE IDENTIFIED. If SO_PEERCRED could not be read or /proc/<pid>/exe could
// not be resolved, this refuses: "I could not tell who you are" is not a reason to hand over a guest's
// credential. The one deliberate exception is a build where this check does not exist at all (see
// peer_identity_other.go), which is not Linux and therefore not the appliance.
func requireEdgedPeer(w http.ResponseWriter, r *http.Request) bool {
	p, ok := peerFrom(r.Context())
	if !ok || !p.Read {
		httpErr(w, http.StatusForbidden,
			"this route requires a caller the server can identify, and the peer credentials could not be read")
		return false
	}
	if p.Exe == "" {
		httpErr(w, http.StatusForbidden,
			"this route requires a caller the server can identify, and the peer's executable could not be resolved")
		return false
	}
	resolved := filepath.Clean(p.Exe)
	for _, allowed := range edgedBinaryPaths() {
		if resolved == filepath.Clean(allowed) {
			return true
		}
		// A release-directory install resolves the symlink, so compare the real path of the allowed
		// location too rather than requiring the caller to have used the symlink.
		if real, err := filepath.EvalSymlinks(allowed); err == nil && resolved == filepath.Clean(real) {
			return true
		}
	}
	// Named in the log, because "a process on this appliance asked for a guest's voucher code" is worth
	// seeing even when it was refused. The code is not disclosed and no reveal row is written.
	slogWarnPeerRefused(p, r.URL.Path)
	httpErr(w, http.StatusForbidden,
		"this route may only be called by edged: it returns a guest credential in the clear, and the "+
			"authorization and password step-up that permit that happen there")
	return false
}

func slogWarnPeerRefused(p peerCred, path string) {
	slog.Warn("REFUSED a code-recovery call from a process that is not edged",
		"path", path, "peer_pid", p.PID, "peer_uid", p.UID, "peer_exe", p.Exe)
}
