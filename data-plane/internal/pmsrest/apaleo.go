package pmsrest

// Apaleo client.
//
// Documentation (verified 2026-09-24):
//   - https://apaleo.dev/guides/oauth-connection/simple-client.html   client credentials: POST
//     https://identity.apaleo.com/connect/token, Authorization: Basic base64(client_id:client_secret),
//     grant_type=client_credentials
//   - https://api.apaleo.com/swagger/booking-v1/swagger.json          GET /booking/v1/reservations
//     (propertyIds, status [Confirmed|InHouse|CheckedOut|Canceled|NoShow], dateFilter [.. Modification ..],
//     from/to, pageNumber, pageSize <= 500; 204 No Content for an empty page; ReservationListModel
//     {reservations[], count}; ReservationItemModel id, status, arrival, departure, modified, unit{id,name},
//     primaryGuest{firstName,lastName}, additionalGuests[])
//   - https://api.apaleo.com/swagger/inventory-v1/swagger.json        GET /inventory/v1/units (propertyId,
//     pageNumber, pageSize <= 500; UnitListModel {units[], count}; UnitItemModel name)
//
// Array query parameters use the Swagger 2 default collection format (comma separated). arrival/departure
// are date-times WITH the property's UTC offset; they are converted to the configured property time zone.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apaleoPage = 500

type apaleoClient struct {
	o            Options
	base         string
	tokenURL     string
	property     string
	clientID     string
	clientSecret string

	token     string
	tokenExp  time.Time
	unitNames []string
}

func (c *apaleoClient) authenticate(ctx context.Context) error {
	if c.token != "" && c.o.Now().Before(c.tokenExp) {
		return nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}.Encode()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	_, err := c.o.do(ctx, "connect/token", func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.tokenURL, strings.NewReader(form))
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.clientID, c.clientSecret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}, &tok)
	if err != nil {
		if KindOf(err) == KindRequest { // 400 invalid_client / unauthorized_client is a credential refusal
			return perr(KindAuth, "connect/token", err.(*Error).Status, nil)
		}
		return err
	}
	if tok.AccessToken == "" {
		return perr(KindInvalidResponse, "connect/token", 200, nil)
	}
	exp := time.Duration(tok.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 5 * time.Minute
	}
	c.token = tok.AccessToken
	c.tokenExp = c.o.Now().Add(exp - exp/10) // renew before the provider's expiry
	return nil
}

// get performs an authenticated GET, re-authenticating ONCE when the API answers 401 (an expired token).
func (c *apaleoClient) get(ctx context.Context, op, path string, q url.Values, out any) (int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.authenticate(ctx); err != nil {
			return 0, err
		}
		status, err := c.o.do(ctx, op, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodGet, c.base+path+"?"+q.Encode(), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+c.token)
			return req, nil
		}, out)
		if KindOf(err) == KindAuth && status == http.StatusUnauthorized && attempt == 0 {
			c.token = "" // force a fresh token and try once more
			continue
		}
		return status, err
	}
	return 0, perr(KindAuth, op, http.StatusUnauthorized, nil)
}

type apaleoGuest struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

type apaleoReservation struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Arrival   string `json:"arrival"`
	Departure string `json:"departure"`
	Modified  string `json:"modified"`
	Unit      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"unit"`
	PrimaryGuest     *apaleoGuest  `json:"primaryGuest"`
	AdditionalGuests []apaleoGuest `json:"additionalGuests"`
}

func (c *apaleoClient) reservations(ctx context.Context, q url.Values, max int) ([]apaleoReservation, error) {
	var out []apaleoReservation
	seen := map[string]bool{}
	size := apaleoPage
	if max > 0 {
		size = max
	}
	for page := 1; page < 100000; page++ {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("propertyIds", c.property)
		qq.Set("pageNumber", strconv.Itoa(page))
		qq.Set("pageSize", strconv.Itoa(size))
		var resp struct {
			Reservations []apaleoReservation `json:"reservations"`
			Count        *int64              `json:"count"`
		}
		status, err := c.get(ctx, "booking/reservations", "/booking/v1/reservations", qq, &resp)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNoContent {
			return out, nil // "If the page has no items, the API returns 204 No Content."
		}
		if resp.Reservations == nil || resp.Count == nil {
			return nil, perr(KindInvalidResponse, "booking/reservations", status, nil)
		}
		for _, r := range resp.Reservations {
			if r.ID != "" && !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
		if max > 0 || len(resp.Reservations) < size || int64(page*size) >= *resp.Count {
			return out, nil
		}
	}
	return out, nil
}

func (c *apaleoClient) units(ctx context.Context) ([]string, error) {
	var names []string
	for page := 1; page < 10000; page++ {
		q := url.Values{"propertyId": {c.property}, "pageNumber": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(apaleoPage)}}
		var resp struct {
			Units []struct {
				Name string `json:"name"`
			} `json:"units"`
			Count *int64 `json:"count"`
		}
		status, err := c.get(ctx, "inventory/units", "/inventory/v1/units", q, &resp)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNoContent {
			return names, nil
		}
		if resp.Units == nil || resp.Count == nil {
			return nil, perr(KindInvalidResponse, "inventory/units", status, nil)
		}
		for _, u := range resp.Units {
			if u.Name != "" {
				names = append(names, u.Name)
			}
		}
		if len(resp.Units) < apaleoPage || int64(page*apaleoPage) >= *resp.Count {
			return names, nil
		}
	}
	return names, nil
}

func (c *apaleoClient) normalise(raw []apaleoReservation) []Reservation {
	out := make([]Reservation, 0, len(raw))
	for _, r := range raw {
		res := Reservation{ID: r.ID, Stamp: r.Modified}
		switch r.Status {
		case "InHouse":
			res.State = StateInHouse
		case "CheckedOut":
			res.State = StateCheckedOut
		}
		if r.Unit != nil {
			res.Room = r.Unit.Name
		}
		if r.PrimaryGuest != nil {
			res.FirstName, res.LastName = r.PrimaryGuest.FirstName, r.PrimaryGuest.LastName
		}
		res.Arrival = localDate(parseInstant(r.Arrival), c.o.Location)
		res.Departure = localDate(parseInstant(r.Departure), c.o.Location)
		// Apaleo guests carry no id, so occupants are keyed by name downstream (the Stay Engine matches an
		// id-less occupant on its normalised name). Listed only when there is more than the primary.
		if len(r.AdditionalGuests) > 0 && r.PrimaryGuest != nil {
			res.Sharers = append(res.Sharers, Guest{FirstName: r.PrimaryGuest.FirstName, LastName: r.PrimaryGuest.LastName, Primary: true})
			for _, g := range r.AdditionalGuests {
				if strings.TrimSpace(g.LastName) == "" && strings.TrimSpace(g.FirstName) == "" {
					continue
				}
				res.Sharers = append(res.Sharers, Guest{FirstName: g.FirstName, LastName: g.LastName})
			}
			if len(res.Sharers) < 2 {
				res.Sharers = nil
			}
		}
		out = append(out, res)
	}
	return out
}

func (c *apaleoClient) Snapshot(ctx context.Context) (Snapshot, error) {
	units, err := c.units(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := c.reservations(ctx, url.Values{"status": {"InHouse"}}, 0)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Reservations: c.normalise(raw), Rooms: units, RoomsKnown: true}, nil
}

func (c *apaleoClient) SupportsChanges() bool { return true }

func (c *apaleoClient) Changes(ctx context.Context, since, until time.Time) ([]Reservation, error) {
	raw, err := c.reservations(ctx, url.Values{
		"status":     {"InHouse,CheckedOut"},
		"dateFilter": {"Modification"},
		"from":       {since.UTC().Format("2006-01-02T15:04:05Z")},
		"to":         {until.UTC().Format("2006-01-02T15:04:05Z")},
	}, 0)
	if err != nil {
		return nil, err
	}
	return c.normalise(raw), nil
}

func (c *apaleoClient) Lookup(ctx context.Context, ids []string) ([]Reservation, error) {
	var out []Reservation
	for _, id := range ids {
		var r apaleoReservation
		status, err := c.get(ctx, "booking/reservation", "/booking/v1/reservations/"+url.PathEscape(id), url.Values{}, &r)
		if err != nil {
			if KindOf(err) == KindRequest && status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		if r.ID != "" {
			out = append(out, c.normalise([]apaleoReservation{r})...)
		}
	}
	return out, nil
}

func (c *apaleoClient) Probe(ctx context.Context) (Probe, error) {
	raw, err := c.reservations(ctx, url.Values{"status": {"InHouse"}}, 1)
	if err != nil {
		return Probe{}, err
	}
	return Probe{SampleReservations: len(raw), Property: c.property}, nil
}
