package pms

// Mews Connector API provider (phase 10).
//
// Mews is a REST-first PMS. Every request is a plain HTTPS POST with a
// JSON body that starts with { ClientToken, AccessToken, Client }.
// There's no persistent link to maintain — which means no Start/Stop
// goroutine is strictly required, but we do spin a small background
// refresher for the room→space-id map so first-login latency stays low
// and occupancy changes propagate without a restart.
//
// Validation flow:
//   1. Resolve the room number to a Mews SpaceId (cached map).
//   2. POST /reservations/getAll with CollidingUtc=now and
//      AssignedResourceIds=[spaceId], States=[Processed,Started].
//   3. For any matching reservation(s), POST /customers/getAll to
//      resolve the guest name(s).
//   4. Hand the candidate set to MatchesQuery — reuses the same
//      first/last/reservation matching rules as FIAS.
//
// Endpoints are overridable via BaseURL so tests can drive the impl
// through an httptest server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	mewsDefaultBaseURL    = "https://api.mews-demo.com"
	mewsSpaceRefreshEvery = 15 * time.Minute
	mewsRequestTimeout    = 10 * time.Second
	// Mews APIs cap list responses; we don't expect many reservations per
	// room at a single instant, but cap to be defensive.
	mewsListLimit = 100
)

type Mews struct {
	providerName string
	cfg          ProviderConfig

	baseURL      string
	clientToken  string // integration identifier (your Platform Client)
	accessToken  string // per-enterprise token
	clientName   string // arbitrary identifier, e.g. "StayConnect 0.1"
	enterpriseID string // Mews enterprise/property UUID (ProviderConfig.PropertyID)

	httpClient *http.Client

	mu          sync.RWMutex
	rooms       map[string]string // "101" → Mews SpaceId
	lastRefresh time.Time
	health      Health

	cancel context.CancelFunc
}

// NewMews returns an unconfigured instance. The loader calls Configure()
// before Start() to apply the pms_providers row.
func NewMews(name string) *Mews {
	return &Mews{
		providerName: name,
		clientName:   "StayConnect 0.1",
		httpClient:   &http.Client{Timeout: mewsRequestTimeout},
		rooms:        map[string]string{},
		health:       Health{Status: "idle"},
	}
}

func (m *Mews) Name() string { return m.providerName }
func (m *Mews) Kind() string { return "mews" }

func (m *Mews) Configure(cfg ProviderConfig) error {
	if cfg.Name != "" {
		m.providerName = cfg.Name
	}
	m.cfg = cfg

	m.baseURL = strings.TrimRight(cfg.Connection.BaseURL, "/")
	if m.baseURL == "" {
		m.baseURL = mewsDefaultBaseURL
	}
	// Mews's auth model expects both tokens. We map:
	//   ClientToken → Extra["client_token"] (integration-wide; same across
	//                 all enterprises that use this integration)
	//   AccessToken → api_key (per-enterprise; rotated independently)
	m.accessToken = cfg.Connection.APIKey
	if v, ok := cfg.Connection.Extra["client_token"].(string); ok {
		m.clientToken = v
	}
	if v, ok := cfg.Connection.Extra["client_name"].(string); ok && v != "" {
		m.clientName = v
	}
	m.enterpriseID = cfg.Connection.PropertyID
	if m.accessToken == "" || m.clientToken == "" {
		return errors.New("mews: api_key (AccessToken) and extra.client_token are required")
	}
	return nil
}

func (m *Mews) Config() ProviderConfig { return m.cfg }

func (m *Mews) Health() Health {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.health
	h.CacheSize = len(m.rooms)
	return h
}

// ----- background refresh loop -----

func (m *Mews) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	go m.runLoop(ctx)
}

func (m *Mews) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Mews) runLoop(ctx context.Context) {
	m.setStatus("connecting", "")
	if err := m.refreshSpaces(ctx); err != nil {
		m.setStatus("degraded", err.Error())
		slog.Warn("mews: initial space load failed", "err", err, "provider", m.providerName)
	} else {
		m.setStatus("connected", "")
	}
	t := time.NewTicker(mewsSpaceRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.refreshSpaces(ctx); err != nil {
				m.setStatus("degraded", err.Error())
			} else {
				m.setStatus("connected", "")
			}
		}
	}
}

func (m *Mews) setStatus(status, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health.Status = status
	if status == "connected" {
		m.health.ConnectedSince = time.Now()
	}
	if errMsg != "" {
		m.health.LastError = errMsg
		m.health.LastErrorAt = time.Now()
	}
}

// refreshSpaces pulls the full Space list for the enterprise and rebuilds
// the room→space-id map. Mews's Spaces are long-lived (they represent
// physical rooms), so this is a cheap periodic refresh.
func (m *Mews) refreshSpaces(ctx context.Context) error {
	type spaceReq struct {
		ClientToken   string   `json:"ClientToken"`
		AccessToken   string   `json:"AccessToken"`
		Client        string   `json:"Client"`
		EnterpriseIds []string `json:"EnterpriseIds,omitempty"`
		Extent        struct {
			Spaces bool `json:"Spaces"`
		} `json:"Extent"`
		Limitation struct {
			Count int `json:"Count"`
		} `json:"Limitation"`
	}
	type spaceResp struct {
		Spaces []struct {
			ID     string `json:"Id"`
			Number string `json:"Number"`
			Name   string `json:"Name"`
		} `json:"Spaces"`
	}
	req := spaceReq{
		ClientToken: m.clientToken,
		AccessToken: m.accessToken,
		Client:      m.clientName,
	}
	if m.enterpriseID != "" {
		req.EnterpriseIds = []string{m.enterpriseID}
	}
	req.Extent.Spaces = true
	req.Limitation.Count = 1000

	var out spaceResp
	if err := m.doPOST(ctx, "/api/connector/v1/spaces/getAll", req, &out); err != nil {
		return err
	}

	next := make(map[string]string, len(out.Spaces))
	for _, s := range out.Spaces {
		key := NormalizeRoom(ApplyRoomFormat(m.cfg.Normalization.RoomFormat, s.Number))
		if key == "" {
			continue
		}
		next[key] = s.ID
	}
	m.mu.Lock()
	m.rooms = next
	m.lastRefresh = time.Now()
	m.mu.Unlock()
	return nil
}

// ----- ValidateGuest -----

func (m *Mews) ValidateGuest(ctx context.Context, q Query) (*Result, error) {
	norm := m.cfg.Normalization
	queryRoom := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, q.RoomNumber))
	if queryRoom == "" {
		return nil, ErrNotFound
	}
	m.mu.RLock()
	spaceID, ok := m.rooms[queryRoom]
	m.mu.RUnlock()
	if !ok {
		// Map miss — refresh once in case we booted before the room was added.
		rctx, rcancel := context.WithTimeout(ctx, mewsRequestTimeout)
		_ = m.refreshSpaces(rctx)
		rcancel()
		m.mu.RLock()
		spaceID, ok = m.rooms[queryRoom]
		m.mu.RUnlock()
		if !ok {
			return nil, ErrNotFound
		}
	}

	res, err := m.fetchReservations(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamFail, err)
	}
	if len(res.Reservations) == 0 {
		return nil, ErrNotFound
	}

	// Gather unique customer ids across the hit reservations.
	seen := map[string]struct{}{}
	var custIDs []string
	for _, r := range res.Reservations {
		for _, cid := range r.CustomerIds {
			if _, done := seen[cid]; !done {
				seen[cid] = struct{}{}
				custIDs = append(custIDs, cid)
			}
		}
		if r.CustomerId != "" {
			if _, done := seen[r.CustomerId]; !done {
				seen[r.CustomerId] = struct{}{}
				custIDs = append(custIDs, r.CustomerId)
			}
		}
	}
	custs, err := m.fetchCustomers(ctx, custIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamFail, err)
	}
	byID := make(map[string]mewsCustomer, len(custs))
	for _, c := range custs {
		byID[c.ID] = c
	}

	qFirst := ApplyNameNormalization(norm, q.FirstName)
	qLast := ApplyNameNormalization(norm, q.LastName)
	qRes := ApplyReservationCase(norm, q.ReservationNumber)

	// Check every reservation × customer until one matches the query.
	for _, r := range res.Reservations {
		// Candidate customers for this reservation.
		cids := r.CustomerIds
		if r.CustomerId != "" {
			cids = append(cids, r.CustomerId)
		}
		for _, cid := range cids {
			c, ok := byID[cid]
			if !ok {
				continue
			}
			cFirst := ApplyNameNormalization(norm, c.FirstName)
			cLast := ApplyNameNormalization(norm, c.LastName)
			cRes := ApplyReservationCase(norm, r.Number)
			if !MatchesQuery(q.Mode, qFirst, qLast, qRes, cFirst, cLast, cRes) {
				continue
			}
			display := strings.TrimSpace(c.FirstName + " " + c.LastName)
			return &Result{
				Valid:         true,
				GuestName:     display,
				FirstName:     c.FirstName,
				LastName:      c.LastName,
				Email:         c.Email,
				CheckIn:       r.StartUtc,
				CheckOut:      r.EndUtc,
				RoomNumber:    q.RoomNumber,
				ReservationID: r.Number,
			}, nil
		}
	}
	return nil, ErrNotFound
}

// ----- Cacher + Tester -----

func (m *Mews) CacheSnapshot(limit int) []Reservation {
	// We don't cache reservations (too volatile); return the room map as
	// stub Reservation rows so admins can sanity-check connectivity.
	if limit <= 0 {
		limit = 200
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Reservation, 0, len(m.rooms))
	for room := range m.rooms {
		out = append(out, Reservation{RoomNumber: room})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (m *Mews) TestConnection(ctx context.Context) error {
	// Resolve endpoint (any enterprise that echoes back proves auth). Mews
	// provides /enterprises/get for this; using a single-row response
	// keeps the probe cheap.
	body := map[string]any{
		"ClientToken": m.clientToken,
		"AccessToken": m.accessToken,
		"Client":      m.clientName,
	}
	if m.enterpriseID != "" {
		body["EnterpriseIds"] = []string{m.enterpriseID}
	}
	var out map[string]any
	return m.doPOST(ctx, "/api/connector/v1/enterprises/getAll", body, &out)
}

// ----- HTTP helpers -----

type mewsReservation struct {
	ID          string    `json:"Id"`
	Number      string    `json:"Number"`
	CustomerId  string    `json:"CustomerId"`
	CustomerIds []string  `json:"CustomerIds"`
	StartUtc    time.Time `json:"StartUtc"`
	EndUtc      time.Time `json:"EndUtc"`
}
type mewsReservationsResp struct {
	Reservations []mewsReservation `json:"Reservations"`
}

func (m *Mews) fetchReservations(ctx context.Context, spaceID string) (*mewsReservationsResp, error) {
	now := time.Now().UTC()
	body := map[string]any{
		"ClientToken":         m.clientToken,
		"AccessToken":         m.accessToken,
		"Client":              m.clientName,
		"AssignedResourceIds": []string{spaceID},
		"CollidingUtc": map[string]string{
			"StartUtc": now.Format(time.RFC3339),
			"EndUtc":   now.Format(time.RFC3339),
		},
		"States":     []string{"Processed", "Started"},
		"Limitation": map[string]int{"Count": mewsListLimit},
	}
	var out mewsReservationsResp
	if err := m.doPOST(ctx, "/api/connector/v1/reservations/getAll", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type mewsCustomer struct {
	ID        string `json:"Id"`
	FirstName string `json:"FirstName"`
	LastName  string `json:"LastName"`
	Email     string `json:"Email"`
}

func (m *Mews) fetchCustomers(ctx context.Context, ids []string) ([]mewsCustomer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	body := map[string]any{
		"ClientToken": m.clientToken,
		"AccessToken": m.accessToken,
		"Client":      m.clientName,
		"CustomerIds": ids,
	}
	var out struct {
		Customers []mewsCustomer `json:"Customers"`
	}
	if err := m.doPOST(ctx, "/api/connector/v1/customers/getAll", body, &out); err != nil {
		return nil, err
	}
	return out.Customers, nil
}

// doPOST is the single HTTP chokepoint. Every method on this struct goes
// through it, so auth + error shape + latency tracking all live in one
// place.
func (m *Mews) doPOST(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Mews error shape: {Message, Details, ...}. Surface Message.
		var e struct {
			Message string `json:"Message"`
		}
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			return fmt.Errorf("mews status=%d: %s", resp.StatusCode, e.Message)
		}
		return fmt.Errorf("mews status=%d body=%s", resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
