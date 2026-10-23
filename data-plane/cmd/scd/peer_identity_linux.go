//go:build linux

package main

// scd's admin surface is a unix socket (/run/stayconnect/scd.sock, mode 0660, root:stayconnect) whose trust model is that
// edged has already authenticated the operator, checked permission and re-verified the password. That holds only if the
// caller IS edged, so the peer is identified with SO_PEERCRED (uid, gid, pid) and /proc/<pid>/exe, which the kernel
// resolves to the real path of the running image.
//
// portald (stayconnect-portald), hotel-admin (stayconnect-hotel-admin) and edged (stayconnect) run as separate accounts.
// The uid check stops a different account; the exe check stops edged's own account running something that is not edged
// (hotel-admin's node process once shared edged's uid and a group that opens this socket). Neither subsumes the other, and
// neither is a boundary against a compromised peer sharing edged's uid, which can ptrace edged: hence the separate accounts.
// The socket group still admits portald because it needs the guest routes.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type peerKey struct{}

type peerCred struct {
	PID  int32
	UID  uint32
	Exe  string // resolved /proc/<pid>/exe, or "" when it could not be read
	Read bool   // whether the credentials were obtained at all
}

// withPeerCred is the http.Server ConnContext hook: it reads peer credentials at accept time, because by the time a
// handler runs the peer may have exited and /proc/<pid>/exe be gone.
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

func peerFrom(ctx context.Context) (peerCred, bool) {
	p, ok := ctx.Value(peerKey{}).(peerCred)
	return p, ok
}

// edgedBinaryPaths are the images allowed to call the code-recovery routes. The deployed layout is a release directory with a
// symlink, so both spellings are accepted (SCD_EDGED_BINARY overrides). No prefix match under the release root: it would
// admit any binary a packaging step drops there.
func edgedBinaryPaths() []string {
	if v := os.Getenv("SCD_EDGED_BINARY"); v != "" {
		return []string{v}
	}
	// Read off the appliance: systemd's ExecStart and the running process's /proc/<pid>/exe resolve to the same path. A wrong list fails closed.
	return []string{
		"/opt/stayconnect/bin/edged",
		"/usr/local/bin/edged",
	}
}

// requireEdgedPeer refuses a request that did not come from edged, failing closed when SO_PEERCRED or /proc/<pid>/exe cannot
// be read. The one exception is a non-Linux build (peer_identity_other.go), which is not the appliance.
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
	// The same comparison as the administrative gate (requireEdgedExecutable), so the two cannot drift; a release-directory install resolves the symlink.
	if requireEdgedExecutable(p) {
		return true
	}
	// Logged even when refused: the code is not disclosed and no reveal row is written.
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

// requireEdgedPeer compares the executable; requireAdminPeer compares the uid (the meaningful fact now that portald has its
// own user) plus the exe, and covers the whole administrative surface, while requireEdgedPeer covers the code-recovery routes,
// which return a guest credential.
// Root is allowed on purpose: uid 0 can read the DEK from /etc/stayconnect, attach to any process and replace these binaries,
// and netd, acctd, keepalived and the deployment scripts dial this socket as root.

// edgedUID resolves once, from the system, the uid this appliance's edged runs as (SCD_EDGED_USER overrides the account name); the uid is assigned at install time.
var (
	edgedUIDMu     sync.Mutex
	edgedUIDCached bool
	edgedUIDVal    uint32
	edgedUIDErr    error
)

func edgedUID() (uint32, error) {
	edgedUIDMu.Lock()
	defer edgedUIDMu.Unlock()
	if edgedUIDCached {
		return edgedUIDVal, edgedUIDErr
	}
	// Cached on success or failure: a per-request NSS lookup would sit in the path of a backup restore, and a per-request retry turns one misconfiguration into a log flood.
	edgedUIDCached = true

	name := os.Getenv("SCD_EDGED_USER")
	if name == "" {
		name = "stayconnect"
	}
	u, err := user.Lookup(name)
	if err != nil {
		edgedUIDErr = fmt.Errorf("could not resolve the user edged runs as (%q): %w", name, err)
		return 0, edgedUIDErr
	}
	id, perr := strconv.ParseUint(u.Uid, 10, 32)
	if perr != nil {
		edgedUIDErr = fmt.Errorf("user %q has a uid that is not a number (%q): %w", name, u.Uid, perr)
		return 0, edgedUIDErr
	}
	edgedUIDVal = uint32(id)
	return edgedUIDVal, nil
}

// resetEdgedUIDCache exists for tests (a mutex, not sync.Once): admin_surface_linux_test.go points SCD_EDGED_USER at several users in one binary.
func resetEdgedUIDCache() {
	edgedUIDMu.Lock()
	defer edgedUIDMu.Unlock()
	edgedUIDCached, edgedUIDVal, edgedUIDErr = false, 0, nil
}

// requireAdminPeer refuses an administrative request from a process that is neither edged nor root. It fails closed on
// unreadable peer credentials and on a configured user that cannot be resolved at all (a loud outage beats serving a restore to an unidentified caller).
func requireAdminPeer(w http.ResponseWriter, r *http.Request) bool {
	p, ok := peerFrom(r.Context())
	if !ok || !p.Read {
		httpErr(w, http.StatusForbidden,
			"this is an administrative route and the server could not read the caller's credentials")
		return false
	}
	if p.UID == 0 {
		return true
	}
	want, err := edgedUID()
	if err != nil {
		slog.Error("REFUSING every administrative route: scd cannot resolve which user edged runs as",
			"err", err, "path", r.URL.Path, "peer_uid", p.UID, "peer_pid", p.PID)
		httpErr(w, http.StatusForbidden,
			"this is an administrative route and the server cannot currently establish which caller is "+
				"permitted to use it")
		return false
	}
	if p.UID == want {
		// The uid alone is not enough: hotel-admin's node process ran as edged's uid with a group that opens scd.sock. The
		// executable is required too, from the same allowlist as the code-recovery routes; hotel-admin fails on the exe while matching the uid.
		if requireEdgedExecutable(p) {
			return true
		}
		slog.Warn("REFUSED an administrative call from a process sharing edged's uid but running a different image",
			"path", r.URL.Path, "peer_pid", p.PID, "peer_uid", p.UID, "peer_exe", p.Exe)
		httpErr(w, http.StatusForbidden,
			"this is an administrative route and may only be called by edged itself: the caller shares "+
				"edged's account but is not edged")
		return false
	}
	// Logged even though refused: the other processes that can open this socket are the captive portal and the admin UI.
	slog.Warn("REFUSED an administrative call from a process that is neither edged nor root",
		"path", r.URL.Path, "peer_pid", p.PID, "peer_uid", p.UID, "peer_exe", p.Exe, "expected_uid", want)
	httpErr(w, http.StatusForbidden,
		"this is an administrative route and may only be called by edged: the operator authentication, "+
			"permission check and step-up that permit these operations happen there")
	return false
}

// requireEdgedExecutable reports whether the peer's resolved image is the installed edged binary; shared by both gates so they cannot drift.
func requireEdgedExecutable(p peerCred) bool {
	if p.Exe == "" {
		return false // could not be resolved: not a reason to trust it
	}
	resolved := filepath.Clean(p.Exe)
	for _, allowed := range edgedBinaryPaths() {
		if resolved == filepath.Clean(allowed) {
			return true
		}
		if real, err := filepath.EvalSymlinks(allowed); err == nil && resolved == filepath.Clean(real) {
			return true
		}
	}
	return false
}
