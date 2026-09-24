package pmsrest

// Mews Connector API client.
//
// Documentation (verified 2026-09-24):
//   - https://docs.mews.com/connector-api/operations/reservations      reservations/getAll/2023-06-06
//   - https://docs.mews.com/connector-api/operations/customers         customers/getAll
//   - https://docs.mews.com/connector-api/operations/companionships    companionships/getAll
//   - https://docs.mews.com/connector-api/operations/resources         resources/getAll
//   - https://docs.mews.com/connector-api/guidelines/pagination        Limitation {Count 1..1000, Cursor}
//   - https://docs.mews.com/connector-api/guidelines/requests          429 + Retry-After, 408, sliding window
//   - https://docs.mews.com/connector-api/guidelines/environments      https://api.mews.com / api.mews-demo.com
//
// Every operation is a POST of a JSON body carrying ClientToken, AccessToken and Client. Reservation state
// "Started" is in-house and "Processed" is checked out. Times are UTC instants (…Utc) and are converted to
// the property's calendar date here.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const mewsClientName = "StayConnect Enterprise 1.0"

// mewsPage is the Limitation object; Count is at most 1000 per the pagination guideline.
const mewsPage = 1000

// mewsCollidingLookback bounds the CollidingUtc window used to list in-house reservations. A Started
// reservation's interval intersects [now-30d, now+1d] unless its scheduled end is more than 30 days past,
// which would be an in-house guest a month past departure. The API caps any interval at three months.
const mewsCollidingLookback = 30 * 24 * time.Hour

type mewsClient struct {
	o            Options
	base         string
	clientToken  string
	accessToken  string
	enterpriseID string
	serviceID    string

	resources map[string]string // resource id -> name (room number), refreshed on a miss
	rooms     []string          // names of Space resources
}

type mewsInterval struct {
	StartUtc string `json:"StartUtc"`
	EndUtc   string `json:"EndUtc"`
}

type mewsLimitation struct {
	Count  int     `json:"Count"`
	Cursor *string `json:"Cursor,omitempty"`
}

type mewsReservation struct {
	ID                 string  `json:"Id"`
	Number             string  `json:"Number"`
	State              string  `json:"State"`
	AccountID          string  `json:"AccountId"`
	AccountType        string  `json:"AccountType"`
	AssignedResourceID *string `json:"AssignedResourceId"`
	StartUtc           string  `json:"StartUtc"`
	EndUtc             string  `json:"EndUtc"`
	ScheduledStartUtc  string  `json:"ScheduledStartUtc"`
	ActualStartUtc     *string `json:"ActualStartUtc"`
	ScheduledEndUtc    string  `json:"ScheduledEndUtc"`
	ActualEndUtc       *string `json:"ActualEndUtc"`
	UpdatedUtc         string  `json:"UpdatedUtc"`
}

func (c *mewsClient) post(ctx context.Context, op, path string, payload map[string]any, out any) error {
	payload["ClientToken"] = c.clientToken
	payload["AccessToken"] = c.accessToken
	payload["Client"] = mewsClientName
	b, err := json.Marshal(payload)
	if err != nil {
		return perr(KindConfig, op, 0, err)
	}
	_, err = c.o.do(ctx, op, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, out)
	return err
}

func (c *mewsClient) scope(p map[string]any) map[string]any {
	if c.enterpriseID != "" {
		p["EnterpriseIds"] = []string{c.enterpriseID}
	}
	if c.serviceID != "" {
		p["ServiceIds"] = []string{c.serviceID}
	}
	return p
}

// reservations pages reservations/getAll/2023-06-06 with the given filters until the cursor is null or a
// page is short. max > 0 stops after that many (the probe).
func (c *mewsClient) reservations(ctx context.Context, filters map[string]any, max int) ([]mewsReservation, error) {
	var out []mewsReservation
	var cursor *string
	seen := map[string]bool{}
	for page := 0; page < 10000; page++ {
		count := mewsPage
		if max > 0 {
			count = max
		}
		p := c.scope(map[string]any{"Limitation": mewsLimitation{Count: count, Cursor: cursor}})
		for k, v := range filters {
			p[k] = v
		}
		var resp struct {
			Reservations []mewsReservation `json:"Reservations"`
			Cursor       *string           `json:"Cursor"`
		}
		if err := c.post(ctx, "reservations/getAll", "/api/connector/v1/reservations/getAll/2023-06-06", p, &resp); err != nil {
			return nil, err
		}
		if resp.Reservations == nil {
			return nil, perr(KindInvalidResponse, "reservations/getAll", 200, nil)
		}
		for _, r := range resp.Reservations {
			if r.ID == "" || seen[r.ID] {
				continue // a cursor page that repeats an item is not a second reservation
			}
			seen[r.ID] = true
			out = append(out, r)
		}
		if max > 0 || resp.Cursor == nil || *resp.Cursor == "" || len(resp.Reservations) < count {
			return out, nil
		}
		cursor = resp.Cursor
	}
	return out, nil
}

func (c *mewsClient) loadResources(ctx context.Context) error {
	c.resources = map[string]string{}
	c.rooms = nil
	var cursor *string
	for page := 0; page < 1000; page++ {
		p := map[string]any{
			"Extent":     map[string]bool{"Resources": true, "Inactive": false},
			"Limitation": mewsLimitation{Count: mewsPage, Cursor: cursor},
		}
		if c.enterpriseID != "" {
			p["EnterpriseIds"] = []string{c.enterpriseID}
		}
		var resp struct {
			Resources []struct {
				ID       string `json:"Id"`
				Name     string `json:"Name"`
				IsActive *bool  `json:"IsActive"`
				Data     *struct {
					Discriminator string `json:"Discriminator"`
				} `json:"Data"`
			} `json:"Resources"`
			Cursor *string `json:"Cursor"`
		}
		if err := c.post(ctx, "resources/getAll", "/api/connector/v1/resources/getAll", p, &resp); err != nil {
			return err
		}
		if resp.Resources == nil {
			return perr(KindInvalidResponse, "resources/getAll", 200, nil)
		}
		for _, r := range resp.Resources {
			c.resources[r.ID] = r.Name
			active := r.IsActive == nil || *r.IsActive
			if active && r.Name != "" && (r.Data == nil || r.Data.Discriminator == "" || r.Data.Discriminator == "Space") {
				c.rooms = append(c.rooms, r.Name)
			}
		}
		if resp.Cursor == nil || *resp.Cursor == "" || len(resp.Resources) < mewsPage {
			return nil
		}
		cursor = resp.Cursor
	}
	return nil
}

type mewsName struct{ First, Last string }

func (c *mewsClient) customers(ctx context.Context, ids []string) (map[string]mewsName, error) {
	out := map[string]mewsName{}
	for start := 0; start < len(ids); start += mewsPage {
		end := start + mewsPage
		if end > len(ids) {
			end = len(ids)
		}
		var cursor *string
		for page := 0; page < 1000; page++ {
			p := map[string]any{
				"CustomerIds": ids[start:end],
				"Extent":      map[string]bool{"Customers": true, "Documents": false, "Addresses": false},
				"Limitation":  mewsLimitation{Count: mewsPage, Cursor: cursor},
			}
			var resp struct {
				Customers []struct {
					ID        string `json:"Id"`
					FirstName string `json:"FirstName"`
					LastName  string `json:"LastName"`
				} `json:"Customers"`
				Cursor *string `json:"Cursor"`
			}
			if err := c.post(ctx, "customers/getAll", "/api/connector/v1/customers/getAll", p, &resp); err != nil {
				return nil, err
			}
			if resp.Customers == nil {
				return nil, perr(KindInvalidResponse, "customers/getAll", 200, nil)
			}
			for _, cu := range resp.Customers {
				out[cu.ID] = mewsName{First: cu.FirstName, Last: cu.LastName}
			}
			if resp.Cursor == nil || *resp.Cursor == "" || len(resp.Customers) < mewsPage {
				break
			}
			cursor = resp.Cursor
		}
	}
	return out, nil
}

// companions returns reservation id -> companion customer ids (the reservation owner excluded later).
func (c *mewsClient) companions(ctx context.Context, resIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for start := 0; start < len(resIDs); start += mewsPage {
		end := start + mewsPage
		if end > len(resIDs) {
			end = len(resIDs)
		}
		var cursor *string
		for page := 0; page < 1000; page++ {
			p := map[string]any{
				"ReservationIds": resIDs[start:end],
				"Extent":         map[string]bool{"Reservations": false, "ReservationGroups": false, "Customers": false},
				"Limitation":     mewsLimitation{Count: mewsPage, Cursor: cursor},
			}
			var resp struct {
				Companionships []struct {
					CustomerID    string  `json:"CustomerId"`
					ReservationID *string `json:"ReservationId"`
				} `json:"Companionships"`
				Cursor *string `json:"Cursor"`
			}
			if err := c.post(ctx, "companionships/getAll", "/api/connector/v1/companionships/getAll", p, &resp); err != nil {
				return nil, err
			}
			if resp.Companionships == nil {
				return nil, perr(KindInvalidResponse, "companionships/getAll", 200, nil)
			}
			for _, cp := range resp.Companionships {
				if cp.ReservationID != nil && *cp.ReservationID != "" && cp.CustomerID != "" {
					out[*cp.ReservationID] = append(out[*cp.ReservationID], cp.CustomerID)
				}
			}
			if resp.Cursor == nil || *resp.Cursor == "" || len(resp.Companionships) < mewsPage {
				break
			}
			cursor = resp.Cursor
		}
	}
	return out, nil
}

// normalise resolves names, rooms and companions for a batch of raw reservations.
func (c *mewsClient) normalise(ctx context.Context, raw []mewsReservation) ([]Reservation, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// resource names: refresh once if a reservation names a resource we have not seen
	if c.resources == nil {
		if err := c.loadResources(ctx); err != nil {
			return nil, err
		}
	}
	for _, r := range raw {
		if r.AssignedResourceID != nil && *r.AssignedResourceID != "" {
			if _, ok := c.resources[*r.AssignedResourceID]; !ok {
				if err := c.loadResources(ctx); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		ids = append(ids, r.ID)
	}
	comp, err := c.companions(ctx, ids)
	if err != nil {
		return nil, err
	}
	custSet := map[string]bool{}
	var custIDs []string
	add := func(id string) {
		if id != "" && !custSet[id] {
			custSet[id] = true
			custIDs = append(custIDs, id)
		}
	}
	for _, r := range raw {
		if r.AccountType == "" || r.AccountType == "Customer" {
			add(r.AccountID)
		}
		for _, cid := range comp[r.ID] {
			add(cid)
		}
	}
	names, err := c.customers(ctx, custIDs)
	if err != nil {
		return nil, err
	}
	loc := c.o.Location
	out := make([]Reservation, 0, len(raw))
	for _, r := range raw {
		res := Reservation{ID: r.ID, Stamp: r.UpdatedUtc}
		switch r.State {
		case "Started":
			res.State = StateInHouse
		case "Processed":
			res.State = StateCheckedOut
		}
		if r.AssignedResourceID != nil {
			res.Room = c.resources[*r.AssignedResourceID]
		}
		// The reservation's owner is the main guest only when the account is a Customer; a Company-owned
		// reservation names its guests through companionships.
		owner := ""
		if r.AccountType == "" || r.AccountType == "Customer" {
			owner = r.AccountID
		}
		if owner == "" && len(comp[r.ID]) > 0 {
			owner = comp[r.ID][0]
		}
		if n, ok := names[owner]; ok {
			res.FirstName, res.LastName = n.First, n.Last
		}
		arr := r.ScheduledStartUtc
		if r.ActualStartUtc != nil && *r.ActualStartUtc != "" {
			arr = *r.ActualStartUtc
		}
		if arr == "" {
			arr = r.StartUtc
		}
		dep := r.ScheduledEndUtc
		if dep == "" {
			dep = r.EndUtc
		}
		res.Arrival = localDate(parseInstant(arr), loc)
		res.Departure = localDate(parseInstant(dep), loc)
		// Occupants: the owner (primary) plus every companion. Reported only when there is more than the
		// primary to say, so a single-guest stay carries no sharer list at all.
		if cs := comp[r.ID]; len(cs) > 0 {
			seen := map[string]bool{}
			if n, ok := names[owner]; ok && owner != "" {
				res.Sharers = append(res.Sharers, Guest{ExternalID: owner, FirstName: n.First, LastName: n.Last, Primary: true})
				seen[owner] = true
			}
			for _, cid := range cs {
				if seen[cid] {
					continue
				}
				seen[cid] = true
				if n, ok := names[cid]; ok {
					res.Sharers = append(res.Sharers, Guest{ExternalID: cid, FirstName: n.First, LastName: n.Last})
				}
			}
			if len(res.Sharers) < 2 {
				res.Sharers = nil
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func (c *mewsClient) colliding() mewsInterval {
	now := c.o.Now().UTC()
	return mewsInterval{
		StartUtc: now.Add(-mewsCollidingLookback).Format(time.RFC3339),
		EndUtc:   now.Add(24 * time.Hour).Format(time.RFC3339),
	}
}

func (c *mewsClient) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := c.loadResources(ctx); err != nil {
		return Snapshot{}, err
	}
	raw, err := c.reservations(ctx, map[string]any{"States": []string{"Started"}, "CollidingUtc": c.colliding()}, 0)
	if err != nil {
		return Snapshot{}, err
	}
	res, err := c.normalise(ctx, raw)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Reservations: res, Rooms: append([]string(nil), c.rooms...), RoomsKnown: true}, nil
}

func (c *mewsClient) SupportsChanges() bool { return true }

func (c *mewsClient) Changes(ctx context.Context, since, until time.Time) ([]Reservation, error) {
	raw, err := c.reservations(ctx, map[string]any{
		"States":     []string{"Started", "Processed"},
		"UpdatedUtc": mewsInterval{StartUtc: since.UTC().Format(time.RFC3339), EndUtc: until.UTC().Format(time.RFC3339)},
	}, 0)
	if err != nil {
		return nil, err
	}
	return c.normalise(ctx, raw)
}

func (c *mewsClient) Lookup(ctx context.Context, ids []string) ([]Reservation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	raw, err := c.reservations(ctx, map[string]any{"ReservationIds": ids}, 0)
	if err != nil {
		return nil, err
	}
	return c.normalise(ctx, raw)
}

func (c *mewsClient) Probe(ctx context.Context) (Probe, error) {
	raw, err := c.reservations(ctx, map[string]any{"States": []string{"Started"}, "CollidingUtc": c.colliding()}, 1)
	if err != nil {
		return Probe{}, err
	}
	return Probe{SampleReservations: len(raw), Property: c.enterpriseID}, nil
}
