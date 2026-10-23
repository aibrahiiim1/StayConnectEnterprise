package main

// Admission side of the single tc writer. netd's socket is group-readable by every stayconnect service, so the
// producer is authenticated by SO_PEERCRED against one allowlisted uid. netd resolves the Phase-3 flags and its
// own scope itself (phase3_mode.go) so a dark appliance cannot mutate tc; plan currency is in internal/shapeplan.

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

type producerIdentity struct {
	UID int
	PID int
}

type shapingAuthz struct {
	// allowedUID is the acctd service uid; the group would include services that do not own enforcement.
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

func (a shapingAuthz) authorize(id producerIdentity, credErr error) error {
	if !a.configured {
		// Fail closed: without an explicit producer uid acctd cannot be told from any other local process.
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

// planStore persists the last accepted plan so a restart cannot accept an older plan (which would reinstate revoked access).
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

type peerConnKey struct{}

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
