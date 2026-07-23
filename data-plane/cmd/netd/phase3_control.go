package main

// PHASE-3 SHAPING CONTROL PLANE — the admission side of the single tc writer.
//
// Choosing netd as the only code that writes tc (ADR-0002) was necessary but not sufficient. Three things
// still had to be true before a submitted plan could be trusted:
//
//  1. WHO is asking. netd's socket is group-readable by every service in the stayconnect group, so "a local
//     caller" is not an identity. The producer is authenticated by SO_PEERCRED against an exact allowlisted
//     uid — the kernel's statement, not a header, which any local process could write.
//  2. WHETHER Phase 3 is live AT ALL. netd resolves the flags and its own scope (see phase3_mode.go) instead
//     of trusting that acctd would never submit while dark. A dark appliance must be unable to mutate tc even
//     if something upstream is misconfigured.
//  3. WHETHER THIS PLAN IS THE CURRENT ONE. Handled by the shared contract in internal/shapeplan.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// producerIdentity is the authenticated local caller, as the kernel reports it.
type producerIdentity struct {
	UID int
	PID int
}

// shapingAuthz decides whether a caller may submit plans.
type shapingAuthz struct {
	// allowedUID is the dedicated acctd service uid. Exactly one producer is allowed: "any member of the
	// stayconnect group" would include scd, edged, portald and pmsd, none of which own enforcement.
	allowedUID int
	configured bool
}

func newShapingAuthz(getenv func(string) string) shapingAuthz {
	raw := strings.TrimSpace(getenv("NETD_PHASE3_PRODUCER_UID"))
	if raw == "" {
		return shapingAuthz{}
	}
	uid, err := strconv.Atoi(raw)
	if err != nil || uid < 0 {
		return shapingAuthz{}
	}
	return shapingAuthz{allowedUID: uid, configured: true}
}

// authorize accepts only the exact configured producer uid.
func (a shapingAuthz) authorize(id producerIdentity, credErr error) error {
	if !a.configured {
		// Fail closed: without an explicit producer uid there is no way to tell acctd from any other local
		// process, and "probably acctd" is not an authorization decision.
		return errors.New("no Phase-3 shaping producer uid is configured")
	}
	if credErr != nil {
		return fmt.Errorf("peer credentials unreadable: %w", credErr)
	}
	if id.UID != a.allowedUID {
		return fmt.Errorf("uid %d is not the authorized shaping producer", id.UID)
	}
	return nil
}

// planStore persists the last accepted plan so a netd restart cannot be talked into accepting a plan older
// than one it already applied — the exact way a reconciliation loop silently reinstates revoked access.
type planStore struct {
	mu   sync.Mutex
	path string
}

func (s *planStore) load() (shapeplan.Accepted, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return shapeplan.Accepted{}, false
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return shapeplan.Accepted{}, false
	}
	var p shapeplan.Accepted
	if json.Unmarshal(raw, &p) != nil || p.Generation <= 0 {
		return shapeplan.Accepted{}, false
	}
	return p, true
}

func (s *planStore) save(p shapeplan.Accepted) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepathDir(s.path), 0o750)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func filepathDir(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 {
		return p[:i]
	}
	return "."
}

// peerConnKey carries the authenticated identity from the listener into the handler.
type peerConnKey struct{}

// peerListener wraps netd's unix listener so every accepted connection carries its verified peer identity.
type peerListener struct {
	net.Listener
	authz shapingAuthz
}

type peerConn struct {
	net.Conn
	id  producerIdentity
	err error
}

func (l *peerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	id, cerr := peerCredentials(c)
	return &peerConn{Conn: c, id: id, err: cerr}, nil
}
