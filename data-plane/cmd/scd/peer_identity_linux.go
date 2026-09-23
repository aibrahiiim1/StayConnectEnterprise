//go:build linux

package main

// WHO IS ON THE OTHER END OF THE SOCKET, AND WHY IT HAD TO BE ASKED.
//
// scd's admin surface is a unix socket at /run/stayconnect/scd.sock, mode 0660, owned root:stayconnect.
// Its trust model was "edged has already authenticated the operator, checked the resource permission and
// re-verified the password; this socket is not reachable from the network". That model was written for
// license installs and backup runs, and it had a hole that only became serious when this delivery put
// PLAINTEXT GUEST CREDENTIALS behind the same socket. As it stood then:
//
//	edged   ran as stayconnect
//	portald ran as stayconnect      <- the NETWORK-FACING captive portal
//
// Read off the appliance, not assumed: `srw-rw---- root stayconnect /run/stayconnect/scd.sock`, with
// systemd reporting User=stayconnect for both services. So portald could POST to /v1/vouchers/{id}/reveal
// with any real operator UUID and a reason of its choosing and receive the code -- no step-up, no
// permission check, because those happen in edged and nothing proved they had happened.
//
// WHAT THIS CHECK IS, AND WHAT IT HONESTLY IS NOT.
//
// SO_PEERCRED gives the peer's uid, gid and pid. The uid cannot separate edged from portald, because they
// share it. What can be read is the peer's own executable: /proc/<pid>/exe resolves, in the kernel, to the
// real path of the running image. Requiring it to be the installed edged binary -- root-owned and not
// writable by stayconnect -- means portald cannot reach these routes AS ITSELF, and cannot reach them by
// exec'ing a copy it placed somewhere it can write.
//
// IT WAS NOT A BOUNDARY AGAINST A COMPROMISED PEER, and pretending otherwise would have been worse than not
// having it. A process running as stayconnect can ptrace edged unless the kernel forbids it, so under a
// shared uid a full compromise of portald reached edged's memory whatever this function did. This file said
// so, and said the real fix was to stop sharing the uid.
//
// THAT FIX IS NOW DONE, which changes what everything below is worth:
//
//	portald runs as stayconnect-portald    deploy/systemd/stayconnect-portald.service
//	edged   runs as stayconnect            unchanged
//	scd.sock stays root:stayconnect 0660   portald keeps SupplementaryGroups=stayconnect, because it
//	                                       needs the GUEST routes -- commerce, sessions, phase3/5/6
//
// The socket group could not be narrowed to edged alone, which is what the earlier note here proposed: the
// captive portal authenticates guests THROUGH this socket, so removing its access removes guest internet.
// What was narrowed instead is the surface: admin_surface.go classifies every route, and an administrative
// one now requires the peer to be edged BY UID (requireAdminPeer, below). Group membership buys the guest
// surface and nothing more.
//
// So the exe comparison here is no longer the only thing standing between the network-facing process and a
// guest's voucher code -- it is the second of two, and the uid is now the load-bearing one. It is kept
// because a uid check and an image check fail differently: the uid stops a different process, the exe stops
// the same uid running something it should not be running.

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

// ---------------------------------------------------------------------------------------------------------
// THE ADMINISTRATIVE SURFACE, GATED BY UID
// ---------------------------------------------------------------------------------------------------------
//
// requireEdgedPeer above compares the peer's EXECUTABLE, because when it was written edged and portald
// shared a uid and the uid could not tell them apart. That is no longer the arrangement: portald runs as its
// own user (deploy/systemd/stayconnect-portald.service), so the uid is now the meaningful fact and the exe
// is the weaker one -- a process that can exec a copy of edged's path passes an exe check, while nothing it
// can do changes the uid the kernel reports for its own socket.
//
// So the two checks are kept, deliberately, at different strengths for different routes:
//
//	requireAdminPeer  uid only          the whole administrative surface (backups, licence, enrolment)
//	requireEdgedPeer  uid via the gate, plus exe   the code-recovery routes, which return a guest credential
//
// WHY ROOT IS ALLOWED. uid 0 owns the machine: it can read the DEK from /etc/stayconnect, attach to any
// process, and replace these binaries. Refusing root here would buy no security and would break netd,
// acctd, keepalived and the deployment scripts that legitimately dial this socket. It is listed as allowed
// rather than accidentally permitted, because a check that pretends to constrain root is a false claim.

// edgedUID resolves, once, the uid this appliance's edged runs as.
//
// FROM THE SYSTEM, not a constant: the service account's name is a packaging decision (SCD_EDGED_USER
// overrides it for a test or an unusual install) and its uid is assigned at install time -- 998 on this
// appliance, and nothing guarantees that number anywhere else.
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
	// Cached either way, success OR failure. A name-service lookup on every administrative request would
	// put NSS in the path of a backup restore; and a failure that is retried per request turns one
	// misconfiguration into a log flood.
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

// resetEdgedUIDCache exists for the tests, and it is a mutex rather than a sync.Once for exactly that
// reason: admin_surface_linux_test.go points SCD_EDGED_USER at several different users within one test
// binary, and a cache that could not be cleared would make every case after the first assert against a
// stale uid -- passing or failing for a reason unrelated to what it was checking.
func resetEdgedUIDCache() {
	edgedUIDMu.Lock()
	defer edgedUIDMu.Unlock()
	edgedUIDCached, edgedUIDVal, edgedUIDErr = false, 0, nil
}

// requireAdminPeer refuses an administrative request from a process that is not edged and is not root.
//
// FAIL CLOSED, in both of the ways it can fail. If the peer credentials could not be read, it refuses. If
// the configured user cannot be resolved AT ALL, it refuses too and says so in the log -- that condition
// means scd cannot tell who edged is, and serving a licence install or a backup restore to an unidentified
// caller is worse than an outage that is loud and obvious.
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
		return true
	}
	// Worth seeing even though it was refused: on this appliance the only other process in the socket's
	// group is the network-facing captive portal, so this line is what a compromised portal looks like.
	slog.Warn("REFUSED an administrative call from a process that is neither edged nor root",
		"path", r.URL.Path, "peer_pid", p.PID, "peer_uid", p.UID, "peer_exe", p.Exe, "expected_uid", want)
	httpErr(w, http.StatusForbidden,
		"this is an administrative route and may only be called by edged: the operator authentication, "+
			"permission check and step-up that permit these operations happen there")
	return false
}
