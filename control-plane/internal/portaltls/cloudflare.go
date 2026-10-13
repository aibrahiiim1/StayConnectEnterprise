package portaltls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CloudflareAPI is the v4 REST API base.
const CloudflareAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare is the production DNS provider: the onegate.work zone is hosted at Cloudflare (registrar
// GoDaddy). The token should be scoped to that one zone with Zone:Read and DNS:Edit; nothing else.
//
// Records Central writes are DNS-only (proxied=false): a proxied name would resolve to Cloudflare's edge, not
// the appliance's private portal address, and the portal would never be reached.
type Cloudflare struct {
	Token    string // never logged; errors carry Cloudflare's messages, never the request headers
	ZoneName string
	ZoneID   string // cached in portal_tls_settings.zone_id; looked up by name when empty
	BaseURL  string // CloudflareAPI unless a test points elsewhere
	HTTP     *http.Client
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfRecord struct {
	ID      string         `json:"id,omitempty"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Content string         `json:"content,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	TTL     int            `json:"ttl,omitempty"`
	Proxied *bool          `json:"proxied,omitempty"`
}

// do calls the API and decodes result into out. The Authorization header is the only place the token goes.
func (c *Cloudflare) do(ctx context.Context, method, path string, body, out any) error {
	base := c.BaseURL
	if base == "" {
		base = CloudflareAPI
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// net/http errors name the URL, never headers: safe to surface.
		return fmt.Errorf("cloudflare %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("cloudflare %s %s: HTTP %d, unreadable answer", method, path, resp.StatusCode)
	}
	if !env.Success || resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusNotFound && method == http.MethodDelete {
			return errCFNotFound
		}
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return fmt.Errorf("cloudflare %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

var errCFNotFound = errors.New("not found")

type cfZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameServers []string `json:"name_servers"`
	Status      string   `json:"status"`
}

func (c *Cloudflare) zone(ctx context.Context) (cfZone, error) {
	var zones []cfZone
	if err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(c.ZoneName), nil, &zones); err != nil {
		return cfZone{}, err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, c.ZoneName) {
			return z, nil
		}
	}
	return cfZone{}, fmt.Errorf("the token cannot see the zone %s", c.ZoneName)
}

func (c *Cloudflare) zoneID(ctx context.Context) (string, error) {
	if c.ZoneID != "" {
		return c.ZoneID, nil
	}
	z, err := c.zone(ctx)
	if err != nil {
		return "", err
	}
	c.ZoneID = z.ID
	return z.ID, nil
}

func (c *Cloudflare) records(ctx context.Context, typ, name string) ([]cfRecord, error) {
	zid, err := c.zoneID(ctx)
	if err != nil {
		return nil, err
	}
	var out []cfRecord
	err = c.do(ctx, http.MethodGet, "/zones/"+zid+"/dns_records?per_page=100&type="+typ+"&name="+url.QueryEscape(name), nil, &out)
	return out, err
}

func (c *Cloudflare) UpsertA(ctx context.Context, fqdn, ip string) (string, error) {
	zid, err := c.zoneID(ctx)
	if err != nil {
		return "", err
	}
	existing, err := c.records(ctx, "A", fqdn)
	if err != nil {
		return "", err
	}
	off := false
	rec := cfRecord{Type: "A", Name: fqdn, Content: ip, TTL: 300, Proxied: &off}
	var out cfRecord
	if len(existing) == 0 {
		if err := c.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", rec, &out); err != nil {
			return "", err
		}
		return out.ID, nil
	}
	// One name, one address: update the first, remove any strays (a second A would split guests between a
	// live portal address and a dead one).
	if err := c.do(ctx, http.MethodPut, "/zones/"+zid+"/dns_records/"+existing[0].ID, rec, &out); err != nil {
		return "", err
	}
	for _, extra := range existing[1:] {
		if err := c.DeleteRecord(ctx, extra.ID); err != nil {
			return "", err
		}
	}
	return out.ID, nil
}

func (c *Cloudflare) DeleteRecord(ctx context.Context, recordID string) error {
	zid, err := c.zoneID(ctx)
	if err != nil {
		return err
	}
	err = c.do(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+url.PathEscape(recordID), nil, nil)
	if errors.Is(err, errCFNotFound) {
		return nil
	}
	return err
}

func (c *Cloudflare) SetTXT(ctx context.Context, fqdn, value string) (string, error) {
	zid, err := c.zoneID(ctx)
	if err != nil {
		return "", err
	}
	var out cfRecord
	// Quoted: Cloudflare stores TXT content in presentation form and warns about unquoted values.
	rec := cfRecord{Type: "TXT", Name: fqdn, Content: `"` + value + `"`, TTL: 60}
	if err := c.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", rec, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Cloudflare) ListAuthoritativeNS(ctx context.Context) ([]string, error) {
	z, err := c.zone(ctx)
	if err != nil {
		return nil, err
	}
	if len(z.NameServers) == 0 {
		return nil, errors.New("cloudflare reports no nameservers for the zone")
	}
	return z.NameServers, nil
}

// ApplyCAA upserts the two apex CAA records the contract names. The `issue` record for letsencrypt.org is
// updated in place (its accounturi changes when the account does); `issuewild` is forced to ";" (no wildcard
// certificate, ever); every other record -- including other CAs' issue records -- is left alone and reported.
func (c *Cloudflare) ApplyCAA(ctx context.Context, issueValue string) ([]string, error) {
	zid, err := c.zoneID(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := c.records(ctx, "CAA", c.ZoneName)
	if err != nil {
		return nil, err
	}
	want := map[string]string{"issue": issueValue, "issuewild": ";"}
	done := map[string]bool{}
	var others []string
	for _, r := range existing {
		tag, _ := r.Data["tag"].(string)
		val, _ := r.Data["value"].(string)
		switch strings.ToLower(tag) {
		case "issue":
			if caaIssuerDomain(val) != "letsencrypt.org" {
				others = append(others, val)
				continue
			}
		case "issuewild":
		default:
			continue
		}
		t := strings.ToLower(tag)
		if done[t] {
			// A duplicate of a record we own: remove it so exactly one policy remains.
			if err := c.DeleteRecord(ctx, r.ID); err != nil {
				return others, err
			}
			continue
		}
		done[t] = true
		if val == want[t] {
			continue
		}
		upd := cfRecord{Type: "CAA", Name: c.ZoneName, TTL: 3600, Data: map[string]any{"flags": 0, "tag": t, "value": want[t]}}
		if err := c.do(ctx, http.MethodPut, "/zones/"+zid+"/dns_records/"+r.ID, upd, nil); err != nil {
			return others, err
		}
	}
	for _, t := range []string{"issue", "issuewild"} {
		if done[t] {
			continue
		}
		rec := cfRecord{Type: "CAA", Name: c.ZoneName, TTL: 3600, Data: map[string]any{"flags": 0, "tag": t, "value": want[t]}}
		if err := c.do(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", rec, nil); err != nil {
			return others, err
		}
	}
	return others, nil
}

func (c *Cloudflare) Verify(ctx context.Context) (ZoneInfo, error) {
	c.ZoneID = "" // always prove it afresh
	z, err := c.zone(ctx)
	if err != nil {
		return ZoneInfo{}, err
	}
	c.ZoneID = z.ID
	// Reading the records proves the DNS permission, not just Zone:Read.
	if _, err := c.records(ctx, "CAA", c.ZoneName); err != nil {
		return ZoneInfo{}, err
	}
	return ZoneInfo{ID: z.ID, Name: z.Name, NameServers: z.NameServers}, nil
}
