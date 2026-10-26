package main

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	proposalTTL  = 15 * time.Minute
	proposalsMax = 500
)

type routingProposal struct {
	ID       string
	Network  string
	Operator string
	Mode     string
	IDs      []string
	Version  uint64
	State    string
	Expires  time.Time
}

type proposalStore struct {
	mu       sync.Mutex
	items    map[string]*routingProposal
	versions map[string]uint64
	locks    map[string]*sync.Mutex
}

func newProposalStore() *proposalStore {
	return &proposalStore{items: map[string]*routingProposal{}, versions: map[string]uint64{}, locks: map[string]*sync.Mutex{}}
}

func (s *server) proposals() *proposalStore {
	s.propsOnce.Do(func() { s.props = newProposalStore() })
	return s.props
}

func (p *proposalStore) lock(network string) func() {
	p.mu.Lock()
	l := p.locks[network]
	if l == nil {
		l = &sync.Mutex{}
		p.locks[network] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func sameIDs(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

func (p *proposalStore) mint(network, operator, mode string, ids []string, state string, now time.Time) *routingProposal {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.items) >= proposalsMax {
		var oldest string
		for id, it := range p.items {
			if now.After(it.Expires) {
				delete(p.items, id)
			} else if oldest == "" || it.Expires.Before(p.items[oldest].Expires) {
				oldest = id
			}
		}
		if len(p.items) >= proposalsMax && oldest != "" {
			delete(p.items, oldest)
		}
	}
	pr := &routingProposal{ID: hex.EncodeToString(raw[:]), Network: network, Operator: operator, Mode: mode, IDs: append([]string(nil), ids...),
		Version: p.versions[network], State: state, Expires: now.Add(proposalTTL)}
	p.items[pr.ID] = pr
	return pr
}

const (
	propOK       = ""
	propUnknown  = "unknown"
	propOperator = "operator"
	propMismatch = "mismatch"
	propObsolete = "obsolete"
)

func (p *proposalStore) check(id, network, operator, mode string, ids []string, state string, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.items[id]
	switch {
	case id == "" || it == nil || it.Network != network:
		return propUnknown
	case operator == "" || it.Operator != operator:
		return propOperator
	case it.Mode != mode || !sameIDs(it.IDs, ids):
		return propMismatch
	case now.After(it.Expires) || it.Version != p.versions[network] || it.State != state:
		return propObsolete
	}
	return propOK
}

func (p *proposalStore) applied(network string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.versions[network]++
	for id, it := range p.items {
		if it.Network == network {
			delete(p.items, id)
		}
	}
}
