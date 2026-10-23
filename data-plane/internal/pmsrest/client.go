// Package pmsrest holds the read-only HTTPS clients for the polled REST connectors (Mews, Apaleo, OPERA
// Cloud). Each reduces its reservation model to one normalised shape and nothing else leaves the package.
// Errors are typed and carry no response body, URL or credential, so nothing the provider sent reaches a
// log line or database column.
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

type Guest struct {
	ExternalID string
	FirstName  string
	LastName   string
	Primary    bool
}

type Reservation struct {
	ID        string // provider reservation id (the external reservation id)
	State     State
	Room      string // room / unit / resource name; "" when none is assigned
	LastName  string // primary guest
	FirstName string // primary guest
	Arrival   string // YYMMDD in the property's time zone ("" when unknown)
	Departure string // YYMMDD in the property's time zone ("" when unknown)
	Stamp     string // provider change marker (e.g. UpdatedUtc); opaque
	// Sharers lists every named occupant, primary included; nil when none beyond the primary is exposed.
	Sharers []Guest
}

type Snapshot struct {
	Reservations []Reservation
	// Rooms is every enumerated room, or nil when the provider can't enumerate; RoomsKnown tells "no rooms" from "can't tell".
	Rooms      []string
	RoomsKnown bool
}

type Probe struct {
	SampleReservations int
	Property           string
}

// Client is not safe for concurrent use.
type Client interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	// SupportsChanges false means the connector diffs successive snapshots instead.
	SupportsChanges() bool
	// Changes returns reservations modified in [since, until) in any state.
	Changes(ctx context.Context, since, until time.Time) ([]Reservation, error)
	// Lookup confirms departures of reservations that left the in-house list; unknown ids are absent.
	Lookup(ctx context.Context, ids []string) ([]Reservation, error)
	Probe(ctx context.Context) (Probe, error)
}

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

// Error deliberately carries no body, URL or credential.
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

func KindOf(err error) ErrKind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return ""
}

func IsTokenOp(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && (pe.Op == "connect/token" || pe.Op == "oauth/tokens")
}

func perr(kind ErrKind, op string, status int, cause error) *Error {
	return &Error{Kind: kind, Op: op, Status: status, cause: cause}
}

type Options struct {
	HTTP     *http.Client
	Now      func() time.Time
	Location *time.Location // the property's time zone
	// Sleep is injectable so tests don't wait.
	Sleep func(ctx context.Context, d time.Duration) error
	// Retries bounds attempts per request for 429 / 5xx / timeouts (default 3).
	Retries int
	// MaxRetryAfter caps an honoured Retry-After (default 60s).
	MaxRetryAfter time.Duration
	UserAgent     string
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

// do calls mk once per attempt so bodies can be re-sent. It honours 429 Retry-After and retries
// 408/5xx/timeouts with bounded exponential backoff; a 204 leaves out untouched.
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

// parseRetryAfter reads delta-seconds or an HTTP-date.
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

// localDate renders the property's calendar date as FIAS YYMMDD, the form stayengine.parseYYMMDD reads.
func localDate(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("060102")
}

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

// dateOnly does no time-zone arithmetic: a provider's calendar date is already the property's date.
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
