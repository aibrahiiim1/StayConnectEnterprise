package pmsrest

// Oracle OPERA Cloud client, through the Oracle Hospitality Integration Platform (OHIP).
//
// Documentation (verified 2026-09-24 against Oracle's published OpenAPI specifications,
// https://github.com/oracle/hospitality-api-docs):
//   - rest-api-specs/security/v1/publishedoauth.json  POST {gateway}/oauth/v1/tokens, HTTP Basic
//     (client id : client secret), form grant_type=client_credentials & scope, header x-app-key (UUID),
//     optional header enterpriseId; response access_token, expires_in, token_type
//   - rest-api-specs/property/v1/rsv.json             GET {gateway}/rsv/v1/hotels/{hotelId}/reservations
//     (getHotelReservations): headers authorization (Bearer), x-app-key, x-hotelid; query limit, offset,
//     searchType (… InHouse …), reservationIdList (collectionFormat multi); response
//     reservations{reservationInfo[], hasMore, totalResults}; reservationInfo reservationIdList[]{id,type},
//     roomStay{arrivalDate, departureDate, roomId}, reservationGuest{givenName, surname, id},
//     reservationStatus [… InHouse, CheckedOut, DueOut …], lastModifyDateTime (hotel time zone)
//   - https://docs.oracle.com/en/industries/hospitality/integration-platform/
//
// getHotelReservations documents no "modified since" filter, so this client does not implement Changes:
// the connector compares successive in-house lists instead. OPERA's arrival/departure are calendar dates
// already in the hotel's own time zone and are used as-is. The reservation API cannot enumerate rooms.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const operaPage = 100

type operaClient struct {
	o            Options
	base         string
	hotelID      string
	scope        string
	enterpriseID string
	clientID     string
	clientSecret string
	appKey       string

	token    string
	tokenExp time.Time
}

func (c *operaClient) authenticate(ctx context.Context) error {
	if c.token != "" && c.o.Now().Before(c.tokenExp) {
		return nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {c.scope}}.Encode()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	_, err := c.o.do(ctx, "oauth/tokens", func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.base+"/oauth/v1/tokens", strings.NewReader(form))
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.clientID, c.clientSecret)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("x-app-key", c.appKey)
		if c.enterpriseID != "" {
			req.Header.Set("enterpriseId", c.enterpriseID)
		}
		return req, nil
	}, &tok)
	if err != nil {
		if KindOf(err) == KindRequest {
			return perr(KindAuth, "oauth/tokens", err.(*Error).Status, nil)
		}
		return err
	}
	if tok.AccessToken == "" {
		return perr(KindInvalidResponse, "oauth/tokens", 200, nil)
	}
	exp := time.Duration(tok.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = 5 * time.Minute
	}
	c.token = tok.AccessToken
	c.tokenExp = c.o.Now().Add(exp - exp/10)
	return nil
}

type operaID struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type operaReservation struct {
	ReservationIDList []operaID `json:"reservationIdList"`
	RoomStay          *struct {
		ArrivalDate   string `json:"arrivalDate"`
		DepartureDate string `json:"departureDate"`
		RoomID        string `json:"roomId"`
	} `json:"roomStay"`
	ReservationGuest *struct {
		GivenName string `json:"givenName"`
		Surname   string `json:"surname"`
		ID        string `json:"id"`
	} `json:"reservationGuest"`
	ReservationStatus  string `json:"reservationStatus"`
	LastModifyDateTime string `json:"lastModifyDateTime"`
}

func (r operaReservation) id() string {
	for _, i := range r.ReservationIDList {
		if i.Type == "Reservation" && i.ID != "" {
			return i.ID
		}
	}
	return ""
}

func (c *operaClient) list(ctx context.Context, q url.Values, max int) ([]operaReservation, error) {
	var out []operaReservation
	seen := map[string]bool{}
	size := operaPage
	if max > 0 {
		size = max
	}
	offset := 0
	refreshed := false
	for page := 0; page < 10000; page++ {
		if err := c.authenticate(ctx); err != nil {
			return nil, err
		}
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("limit", strconv.Itoa(size))
		qq.Set("offset", strconv.Itoa(offset))
		var resp struct {
			Reservations *struct {
				ReservationInfo []operaReservation `json:"reservationInfo"`
				HasMore         bool               `json:"hasMore"`
			} `json:"reservations"`
		}
		status, err := c.o.do(ctx, "rsv/reservations", func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodGet,
				c.base+"/rsv/v1/hotels/"+url.PathEscape(c.hotelID)+"/reservations?"+qq.Encode(), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+c.token)
			req.Header.Set("x-app-key", c.appKey)
			req.Header.Set("x-hotelid", c.hotelID)
			return req, nil
		}, &resp)
		if KindOf(err) == KindAuth && status == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.token = "" // expired token: one fresh sign-in, then this page again
			if err2 := c.authenticate(ctx); err2 != nil {
				return nil, err2
			}
			page--
			continue
		}
		if err != nil {
			return nil, err
		}
		if status == http.StatusNoContent {
			return out, nil
		}
		if resp.Reservations == nil {
			return nil, perr(KindInvalidResponse, "rsv/reservations", status, nil)
		}
		for _, r := range resp.Reservations.ReservationInfo {
			id := r.id()
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, r)
			}
		}
		n := len(resp.Reservations.ReservationInfo)
		if max > 0 || !resp.Reservations.HasMore || n == 0 {
			return out, nil
		}
		offset += n
	}
	return out, nil
}

func (c *operaClient) normalise(raw []operaReservation) []Reservation {
	out := make([]Reservation, 0, len(raw))
	for _, r := range raw {
		res := Reservation{ID: r.id(), Stamp: r.LastModifyDateTime}
		switch r.ReservationStatus {
		case "InHouse", "DueOut":
			res.State = StateInHouse // DueOut is an in-house guest expected to leave today
		case "CheckedOut":
			res.State = StateCheckedOut
		}
		if r.RoomStay != nil {
			res.Room = r.RoomStay.RoomID
			res.Arrival = dateOnly(r.RoomStay.ArrivalDate)
			res.Departure = dateOnly(r.RoomStay.DepartureDate)
		}
		if r.ReservationGuest != nil {
			res.FirstName, res.LastName = r.ReservationGuest.GivenName, r.ReservationGuest.Surname
		}
		out = append(out, res)
	}
	return out
}

func (c *operaClient) Snapshot(ctx context.Context) (Snapshot, error) {
	raw, err := c.list(ctx, url.Values{"searchType": {"InHouse"}}, 0)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Reservations: c.normalise(raw)}, nil
}

func (c *operaClient) SupportsChanges() bool { return false }

func (c *operaClient) Changes(context.Context, time.Time, time.Time) ([]Reservation, error) {
	return nil, perr(KindConfig, "rsv/changes", 0, nil)
}

func (c *operaClient) Lookup(ctx context.Context, ids []string) ([]Reservation, error) {
	var out []Reservation
	for start := 0; start < len(ids); start += 50 {
		end := start + 50
		if end > len(ids) {
			end = len(ids)
		}
		raw, err := c.list(ctx, url.Values{"reservationIdList": ids[start:end]}, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, c.normalise(raw)...)
	}
	return out, nil
}

func (c *operaClient) Probe(ctx context.Context) (Probe, error) {
	raw, err := c.list(ctx, url.Values{"searchType": {"InHouse"}}, 1)
	if err != nil {
		return Probe{}, err
	}
	return Probe{SampleReservations: len(raw), Property: c.hotelID}, nil
}
