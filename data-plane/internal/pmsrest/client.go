// Package pmsrest holds the HTTPS clients for the polled REST PMS connectors (Mews, Apaleo, OPERA Cloud).
//
// Every client reduces its provider's reservation model to the ONE shape the connector admits: a reservation
// id, the room it occupies, the primary guest's names, its arrival and departure as property-local calendar
// dates in the FIAS "YYMMDD" form the Stay Engine parses, an in-house / checked-out / other state, and an
// opaque change stamp. Nothing else leaves this package, and nothing here writes to a PMS: every call is a
// read.
//
// Errors are TYPED and carry no response body, URL or credential, so they can be classified into bounded
// runtime codes without anything the provider sent reaching a log line or a database column.
package pmsrest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

// State is the connector-relevant reservation state.
type State int

const (
	StateOther State = iota
	StateInHouse
	StateCheckedOut
)

func (s State) String() string {
	switch s {
	case StateInHouse:
		return "IN_HOUSE"
	case StateCheckedOut:
		return "CHECKED_OUT"
	}
	return "OTHER"
}

// Guest is one occupant of a reservation as the provider reports it.
type Guest struct {
	ExternalID string
	FirstName  string
	LastName   string
	Primary    bool
}

// Reservation is the normalised reservation every provider client produces.
type Reservation struct {
	ID        string // provider reservation id (the external reservation id)
	State     State
	Room      string // room / unit / resource name; "" when none is assigned
	LastName  string // primary guest
	FirstName string // primary guest
	Arrival   string // YYMMDD in the property's time zone ("" when unknown)
	Departure string // YYMMDD in the property's time zone ("" when unknown)
	Stamp     string // provider change marker (e.g. UpdatedUtc); opaque
	// Sharers lists every occupant the provider names, the primary included. Nil when the provider exposes
	// no occupant list beyond the primary guest.
	Sharers []Guest
}

// Snapshot is a complete in-house roster.
type Snapshot struct {
	Reservations []Reservation
	// Rooms is every room the provider enumerates for the property (occupied or not), or nil when the provider
	// cannot enumerate rooms. RoomsKnown distinguishes "no rooms" from "cannot tell".
	Rooms      []string
	RoomsKnown bool
}

// Probe is the outcome of a bounded test read.
type Probe struct {
	SampleReservations int
	Property           string
}

// Client is one provider connection. Implementations are safe for use by one goroutine at a time.
type Client interface {
	// Snapshot pages through every in-house reservation.
	Snapshot(ctx context.Context) (Snapshot, error)
	// SupportsChanges reports whether Changes is implemented; when false the connector finds changes by
	// comparing successive snapshots.
	SupportsChanges() bool
	// Changes returns reservations modified in [since, until) in any connector-relevant state.
	Changes(ctx context.Context, since, until time.Time) ([]Reservation, error)
	// Lookup returns the current state of specific reservations (used to confirm a departure when a
	// reservation disappears from the in-house list). Unknown ids are simply absent from the result.
	Lookup(ctx context.Context, ids []string) ([]Reservation, error)
	// Probe authenticates and performs ONE bounded read (page size 1).
	Probe(ctx context.Context) (Probe, error)
}

// ---------- errors ----------

// ErrKind is the bounded classification of a provider failure.
type ErrKind string

const (
	KindAuth            ErrKind = "AUTH"             // 401/403, or a token request refused
	KindRateLimited     ErrKind = "RATE_LIMITED"     // 429 after the bounded retries
	KindTimeout         ErrKind = "TIMEOUT"          // client timeout, or HTTP 408 after retries
	KindUnavailable     ErrKind = "UNAVAILABLE"      // 5xx or a network failure after retries
	KindInvalidResponse ErrKind = "INVALID_RESPONSE" // malformed or unexpected body
	KindRequest         ErrKind = "REQUEST_REJECTED" // other 4xx: the provider refused the request shape
	KindConfig          ErrKind = "CONFIG"           // unusable configuration or credential
)

// Error is a provider failure. It deliberately carries no body, URL or credential.
type Error struct {
	Kind   ErrKind
	Status int    // HTTP status, 0 when none
	Op     string // bounded operation name, e.g. "reservations/getAll"
	cause  error
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("pms provider %s: %s (HTTP %d)", e.Op, e.Kind, e.Status)
	}
	return fmt.Sprintf("pms provider %s: %s", e.Op, e.Kind)
}
func (e *Error) Unwrap() error { return e.cause }

// KindOf returns the classification of err, or "" when it is not a provider error.
func KindOf(err error) ErrKind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return ""
}

// IsTokenOp reports whether err happened while obtaining an access token (as opposed to reading data).
func IsTokenOp(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && (pe.Op == "connect/token" || pe.Op == "oauth/tokens")
}

func perr(kind ErrKind, op string, status int, cause error) *Error {
	return &Error{Kind: kind, Op: op, Status: status, cause: cause}
}

// ---------- HTTP plumbing ----------

// Options are the injectable effects every client uses.
type Options struct {
	HTTP     *http.Client
	Now      func() time.Time
	Location *time.Location // the property's time zone
	// Sleep waits between retries; tests inject a recorder so no test waits real time.
	Sleep func(ctx context.Context, d time.Duration) error
	// Retries bounds the attempts per request for 429 / 5xx / timeouts (default 3).
	Retries int
	// MaxRetryAfter caps an honoured Retry-After (default 60s).
	MaxRetryAfter time.Duration
	// UserAgent identifies this client to the provider.
	UserAgent string
}

func (o *Options) defaults() {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if o.Retries <= 0 {
		o.Retries = 3
	}
	if o.MaxRetryAfter <= 0 {
		o.MaxRetryAfter = 60 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = "StayConnect-Enterprise-PMS-Connector/1.0"
	}
}

const maxBody = 32 << 20

// do sends a request built by mk (called once per attempt so bodies can be re-sent) and decodes a JSON
// response into out. It honours 429 Retry-After and retries 408/5xx/network timeouts with bounded
// exponential backoff. A 204 leaves out untouched and returns (204, nil).
func (o *Options) do(ctx context.Context, op string, mk func() (*http.Request, error), out any) (int, error) {
	backoff := time.Second
	var last error
	for attempt := 1; attempt <= o.Retries; attempt++ {
		req, err := mk()
		if err != nil {
			return 0, perr(KindConfig, op, 0, err)
		}
		req = req.WithContext(ctx)
		req.Header.Set("User-Agent", o.UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := o.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			kind := KindUnavailable
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				kind = KindTimeout
			}
			last = perr(kind, op, 0, err)
			if attempt < o.Retries {
				if serr := o.Sleep(ctx, backoff); serr != nil {
					return 0, serr
				}
				backoff *= 2
				continue
			}
			return 0, last
		}
		status := resp.StatusCode
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		retryAfter := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		switch {
		case status == http.StatusNoContent:
			return status, nil
		case status >= 200 && status < 300:
			if rerr != nil {
				return status, perr(KindUnavailable, op, status, rerr)
			}
			if len(body) > maxBody {
				return status, perr(KindInvalidResponse, op, status, errors.New("response too large"))
			}
			if out != nil {
				if err := json.Unmarshal(body, out); err != nil {
					return status, perr(KindInvalidResponse, op, status, errors.New("malformed JSON"))
				}
			}
			return status, nil
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return status, perr(KindAuth, op, status, nil)
		case status == http.StatusTooManyRequests:
			last = perr(KindRateLimited, op, status, nil)
			if attempt < o.Retries {
				wait := parseRetryAfter(retryAfter, o.Now())
				if wait <= 0 {
					wait = backoff
				}
				if wait > o.MaxRetryAfter {
					wait = o.MaxRetryAfter
				}
				if serr := o.Sleep(ctx, wait); serr != nil {
					return 0, serr
				}
				backoff *= 2
				continue
			}
			return status, last
		case status == http.StatusRequestTimeout || status >= 500:
			kind := KindUnavailable
			if status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout {
				kind = KindTimeout
			}
			last = perr(kind, op, status, nil)
			if attempt < o.Retries {
				if serr := o.Sleep(ctx, backoff); serr != nil {
					return 0, serr
				}
				backoff *= 2
				continue
			}
			return status, last
		default:
			return status, perr(KindRequest, op, status, nil)
		}
	}
	return 0, last
}

// parseRetryAfter reads either delta-seconds or an HTTP-date (RFC 9110 §10.2.3).
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}

// ---------- normalisation helpers ----------

// localDate renders an instant as the property's calendar date in the FIAS YYMMDD form the Stay Engine
// parses (stayengine.parseYYMMDD reads "060102" as a naive date). A zero time yields "".
func localDate(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("060102")
}

// parseInstant accepts RFC 3339 with or without fractional seconds; "" or unparseable yields zero.
func parseInstant(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// dateOnly converts an ISO calendar date (YYYY-MM-DD, possibly followed by a time) to YYMMDD WITHOUT any
// time-zone arithmetic: a provider that reports a calendar date already reports the property's date.
func dateOnly(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return ""
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return ""
	}
	return t.Format("060102")
}

// New builds the client for a REST connector kind from its stored provider configuration and decrypted
// credential JSON. The credential bytes are parsed here and not retained beyond the client's fields.
func New(kind string, provider map[string]any, secret []byte, o Options) (Client, error) {
	o.defaults()
	var cred map[string]string
	if err := json.Unmarshal(secret, &cred); err != nil || cred == nil {
		return nil, perr(KindConfig, "credential", 0, errors.New("credential is not a JSON object"))
	}
	s := func(k string) string { v, _ := provider[k].(string); return v }
	switch kind {
	case pmsprovider.KindMews:
		if cred["client_token"] == "" || cred["access_token"] == "" {
			return nil, perr(KindConfig, "credential", 0, errors.New("mews credential incomplete"))
		}
		return &mewsClient{o: o, base: s("platform_url"), clientToken: cred["client_token"],
			accessToken: cred["access_token"], enterpriseID: s("enterprise_id"), serviceID: s("service_id")}, nil
	case pmsprovider.KindApaleo:
		if cred["client_id"] == "" || cred["client_secret"] == "" {
			return nil, perr(KindConfig, "credential", 0, errors.New("apaleo credential incomplete"))
		}
		return &apaleoClient{o: o, base: s("api_url"), tokenURL: s("identity_url"), property: s("property_id"),
			clientID: cred["client_id"], clientSecret: cred["client_secret"]}, nil
	case pmsprovider.KindOperaCloud:
		if cred["client_id"] == "" || cred["client_secret"] == "" || cred["app_key"] == "" {
			return nil, perr(KindConfig, "credential", 0, errors.New("opera cloud credential incomplete"))
		}
		return &operaClient{o: o, base: s("gateway_url"), hotelID: s("hotel_id"), scope: s("scope"),
			enterpriseID: s("enterprise_id"), clientID: cred["client_id"], clientSecret: cred["client_secret"],
			appKey: cred["app_key"]}, nil
	}
	return nil, perr(KindConfig, "kind", 0, errors.New("not a REST connector kind"))
}
