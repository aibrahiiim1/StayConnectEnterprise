package pms

// Apaleo Connector API provider (phase 11).
//
// Apaleo is REST like Mews but authenticates with OAuth 2.0
// client-credentials — we POST client_id + client_secret to an identity
// endpoint and cache the resulting access_token until shortly before
// expiry. Every API call then carries `Authorization: Bearer <token>`.
//
// Validation flow:
//   1. Resolve the room number to an Apaleo unit ID (cached map, same
//      pattern as Mews).
//   2. GET /booking/v1/reservations?propertyId=X&timeFilter=OverlappingStay
//      &from=now&to=now&unitIds=<id>&expand=primaryGuest
//      — Apaleo inlines the primary guest by default so we don't need a
//      second round-trip.
//   3. Match the inlined guest against the query using the shared
//      MatchesQuery helper.
//
// Config mapping (pms_providers columns → Apaleo concepts):
//   base_url              → API host (default https://api.apaleo.com)
//   api_key               → client_secret
//   extra.client_id       → client_id
//   extra.identity_url    → override for tests (default https://identity.apaleo.com)
//   extra.scopes          → space-separated scopes (default "reservations.read
//                                                         inventory.read")
//   property_id           → Apaleo property code (3-letter uppercase, e.g. "HTL")

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	apaleoDefaultAPIBase      = "https://api.apaleo.com"
	apaleoDefaultIdentityBase = "https://identity.apaleo.com"
	apaleoDefaultScopes       = "reservations.read inventory.read"
	apaleoUnitRefreshEvery    = 15 * time.Minute
	apaleoRequestTimeout      = 10 * time.Second
	apaleoUnitPageSize        = 100
	// Refresh the access token this much before its advertised expiry so
	// a concurrent request never hits a 401.
	apaleoTokenSkew = 60 * time.Second
)

type Apaleo struct {
	providerName string
	cfg          ProviderConfig

	apiBase      string
	identityURL  string
	clientID     string
	clientSecret string
	propertyID   string
	scopes       string

	httpClient *http.Client

	mu          sync.RWMutex
	units       map[string]string // room number → unit id
	token       string
	tokenExpiry time.Time
	lastRefresh time.Time
	health      Health

	cancel context.CancelFunc
}

func NewApaleo(name string) *Apaleo {
	return &Apaleo{
		providerName: name,
		httpClient:   &http.Client{Timeout: apaleoRequestTimeout},
		units:        map[string]string{},
		health:       Health{Status: "idle"},
	}
}

func (a *Apaleo) Name() string { return a.providerName }
func (a *Apaleo) Kind() string { return "apaleo" }

func (a *Apaleo) Configure(cfg ProviderConfig) error {
	if cfg.Name != "" {
		a.providerName = cfg.Name
	}
	a.cfg = cfg

	a.apiBase = strings.TrimRight(cfg.Connection.BaseURL, "/")
	if a.apiBase == "" {
		a.apiBase = apaleoDefaultAPIBase
	}
	a.clientSecret = cfg.Connection.APIKey
	a.propertyID = cfg.Connection.PropertyID

	// Pull optional Apaleo-specific knobs out of Extra.
	a.clientID, _ = cfg.Connection.Extra["client_id"].(string)
	if v, ok := cfg.Connection.Extra["identity_url"].(string); ok && v != "" {
		a.identityURL = strings.TrimRight(v, "/")
	} else {
		a.identityURL = apaleoDefaultIdentityBase
	}
	if v, ok := cfg.Connection.Extra["scopes"].(string); ok && v != "" {
		a.scopes = v
	} else {
		a.scopes = apaleoDefaultScopes
	}

	if a.clientID == "" || a.clientSecret == "" || a.propertyID == "" {
		return errors.New("apaleo: extra.client_id, api_key (client_secret), and property_id are required")
	}
	return nil
}

func (a *Apaleo) Config() ProviderConfig { return a.cfg }

func (a *Apaleo) Health() Health {
	a.mu.RLock()
	defer a.mu.RUnlock()
	h := a.health
	h.CacheSize = len(a.units)
	return h
}

// ----- lifecycle -----

func (a *Apaleo) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	go a.runLoop(ctx)
}

func (a *Apaleo) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *Apaleo) runLoop(ctx context.Context) {
	a.setStatus("connecting", "")
	if err := a.refreshUnits(ctx); err != nil {
		a.setStatus("degraded", err.Error())
		slog.Warn("apaleo: initial unit load failed", "err", err, "provider", a.providerName)
	} else {
		a.setStatus("connected", "")
	}
	t := time.NewTicker(apaleoUnitRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.refreshUnits(ctx); err != nil {
				a.setStatus("degraded", err.Error())
			} else {
				a.setStatus("connected", "")
			}
		}
	}
}

func (a *Apaleo) setStatus(status, errMsg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.health.Status = status
	if status == "connected" {
		a.health.ConnectedSince = time.Now()
	}
	if errMsg != "" {
		a.health.LastError = errMsg
		a.health.LastErrorAt = time.Now()
	}
}

// ----- token cache -----

type apaleoTokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func (a *Apaleo) getToken(ctx context.Context) (string, error) {
	a.mu.RLock()
	if a.token != "" && time.Now().Before(a.tokenExpiry) {
		t := a.token
		a.mu.RUnlock()
		return t, nil
	}
	a.mu.RUnlock()

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", a.clientID)
	form.Set("client_secret", a.clientSecret)
	form.Set("scope", a.scopes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.identityURL+"/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("apaleo token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Apaleo identity errors follow the OAuth2 error shape
		// {error, error_description}; surface the description verbatim.
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return "", fmt.Errorf("apaleo token %s: %s", e.Error, e.Description)
		}
		return "", fmt.Errorf("apaleo token status=%d body=%s", resp.StatusCode, string(b))
	}
	var tk apaleoTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tk); err != nil {
		return "", fmt.Errorf("apaleo token decode: %w", err)
	}
	if tk.AccessToken == "" {
		return "", errors.New("apaleo: empty access_token")
	}
	expiresIn := time.Duration(tk.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 10 * time.Minute
	}
	a.mu.Lock()
	a.token = tk.AccessToken
	a.tokenExpiry = time.Now().Add(expiresIn - apaleoTokenSkew)
	a.mu.Unlock()
	return tk.AccessToken, nil
}

// ----- unit / reservation fetches -----

type apaleoUnit struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type apaleoUnitsResp struct {
	Units []apaleoUnit `json:"units"`
	Count int          `json:"count"`
}

func (a *Apaleo) refreshUnits(ctx context.Context) error {
	next := make(map[string]string, 64)
	// Apaleo paginates with pageNumber starting at 1. Stop when we get
	// fewer than pageSize rows (or run past a safety cap to avoid an
	// infinite loop on a broken server).
	for page := 1; page <= 50; page++ {
		q := url.Values{}
		q.Set("propertyId", a.propertyID)
		q.Set("pageNumber", fmt.Sprintf("%d", page))
		q.Set("pageSize", fmt.Sprintf("%d", apaleoUnitPageSize))

		var out apaleoUnitsResp
		if err := a.doGET(ctx, "/inventory/v1/units?"+q.Encode(), &out); err != nil {
			return err
		}
		for _, u := range out.Units {
			key := NormalizeRoom(ApplyRoomFormat(a.cfg.Normalization.RoomFormat, u.Name))
			if key == "" {
				continue
			}
			next[key] = u.ID
		}
		if len(out.Units) < apaleoUnitPageSize {
			break
		}
	}
	a.mu.Lock()
	a.units = next
	a.lastRefresh = time.Now()
	a.mu.Unlock()
	return nil
}

type apaleoPrimaryGuest struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
}
type apaleoReservation struct {
	ID           string             `json:"id"`
	BookingID    string             `json:"bookingId"`
	Arrival      time.Time          `json:"arrival"`
	Departure    time.Time          `json:"departure"`
	Status       string             `json:"status"`
	PrimaryGuest apaleoPrimaryGuest `json:"primaryGuest"`
}
type apaleoReservationsResp struct {
	Reservations []apaleoReservation `json:"reservations"`
	Count        int                 `json:"count"`
}

func (a *Apaleo) fetchReservations(ctx context.Context, unitID string) (*apaleoReservationsResp, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	q := url.Values{}
	q.Set("propertyId", a.propertyID)
	q.Set("timeFilter", "OverlappingStay")
	q.Set("from", now)
	q.Set("to", now)
	q.Set("unitIds", unitID)
	q.Set("expand", "primaryGuest")

	var out apaleoReservationsResp
	if err := a.doGET(ctx, "/booking/v1/reservations?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- ValidateGuest -----

func (a *Apaleo) ValidateGuest(ctx context.Context, q Query) (*Result, error) {
	norm := a.cfg.Normalization
	queryRoom := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, q.RoomNumber))
	if queryRoom == "" {
		return nil, ErrNotFound
	}
	a.mu.RLock()
	unitID, ok := a.units[queryRoom]
	a.mu.RUnlock()
	if !ok {
		rctx, cancel := context.WithTimeout(ctx, apaleoRequestTimeout)
		_ = a.refreshUnits(rctx)
		cancel()
		a.mu.RLock()
		unitID, ok = a.units[queryRoom]
		a.mu.RUnlock()
		if !ok {
			return nil, ErrNotFound
		}
	}

	resv, err := a.fetchReservations(ctx, unitID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamFail, err)
	}
	if len(resv.Reservations) == 0 {
		return nil, ErrNotFound
	}

	qFirst := ApplyNameNormalization(norm, q.FirstName)
	qLast := ApplyNameNormalization(norm, q.LastName)
	qRes := ApplyReservationCase(norm, q.ReservationNumber)

	for _, r := range resv.Reservations {
		rFirst := ApplyNameNormalization(norm, r.PrimaryGuest.FirstName)
		rLast := ApplyNameNormalization(norm, r.PrimaryGuest.LastName)
		// Apaleo's bookingId is the human-facing identifier on receipts,
		// so we match reservation_number against that (not the internal id).
		rResNum := ApplyReservationCase(norm, r.BookingID)
		if !MatchesQuery(q.Mode, qFirst, qLast, qRes, rFirst, rLast, rResNum) {
			continue
		}
		display := strings.TrimSpace(r.PrimaryGuest.FirstName + " " + r.PrimaryGuest.LastName)
		return &Result{
			Valid:         true,
			GuestName:     display,
			FirstName:     r.PrimaryGuest.FirstName,
			LastName:      r.PrimaryGuest.LastName,
			Email:         r.PrimaryGuest.Email,
			CheckIn:       r.Arrival,
			CheckOut:      r.Departure,
			RoomNumber:    q.RoomNumber,
			ReservationID: r.BookingID,
		}, nil
	}
	return nil, ErrNotFound
}

// ----- Cacher + Tester -----

func (a *Apaleo) CacheSnapshot(limit int) []Reservation {
	if limit <= 0 {
		limit = 200
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Reservation, 0, len(a.units))
	for room := range a.units {
		out = append(out, Reservation{RoomNumber: room})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// TestConnection proves the full auth loop — we fetch a token AND make one
// authenticated call (units page 1, smallest possible response).
func (a *Apaleo) TestConnection(ctx context.Context) error {
	if _, err := a.getToken(ctx); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("propertyId", a.propertyID)
	q.Set("pageSize", "1")
	var out apaleoUnitsResp
	return a.doGET(ctx, "/inventory/v1/units?"+q.Encode(), &out)
}

// ----- HTTP helper -----

// doGET wraps the bearer-token fetch + API call. Same chokepoint rule as
// Mews's doPOST: auth header, error shape, response decode all live here.
func (a *Apaleo) doGET(ctx context.Context, path string, out any) error {
	tok, err := a.getToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// Token got invalidated mid-flight (credential rotation) — drop
		// the cache and bubble up; the caller can retry which triggers
		// a fresh token fetch on the next call.
		a.mu.Lock()
		a.token = ""
		a.tokenExpiry = time.Time{}
		a.mu.Unlock()
		return fmt.Errorf("apaleo %s: 401 (token invalidated)", path)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("apaleo %s: status=%d body=%s", path, resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
