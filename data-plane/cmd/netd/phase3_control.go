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
	"time"

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

// load returns the stored plan. A missing file is "no history" (false, nil); a file that exists but cannot be read
// or parsed is an error: the replay guard it held is unknown, which the caller reports (audit Phase 8 M8).
func (s *planStore) load() (shapeplan.Accepted, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return shapeplan.Accepted{}, false, nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return shapeplan.Accepted{}, false, nil
	}
	if err != nil {
		return shapeplan.Accepted{}, false, fmt.Errorf("accepted-plan record unreadable: %w", err)
	}
	var p shapeplan.Accepted
	if err := json.Unmarshal(raw, &p); err != nil || p.Generation <= 0 {
		return shapeplan.Accepted{}, false, fmt.Errorf("accepted-plan record is corrupt (%d bytes)", len(raw))
	}
	return p, true, nil
}

// save writes the accepted plan durably (temp file, fsync, rename, directory fsync) and reports a failure: a lost
// write is a lost replay guard, so the caller must not report the plan converged (audit Phase 8 M8).
func (s *planStore) save(p shapeplan.Accepted) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, raw, 0o600)
}

// maxPlanValidity caps how far ahead a plan may claim to be current. acctd issues 90 s plans; a far-future expiry
// would keep an old plan replayable for as long as it claims, should the replay guard be lost (audit Phase 8 M8).
const maxPlanValidity = 10 * time.Minute

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
