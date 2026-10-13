package portaltls

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ZoneInfo is what a DNS provider reports about the zone Central manages. Non-secret.
type ZoneInfo struct {
	ID          string   `json:"zone_id"`
	Name        string   `json:"zone_name"`
	NameServers []string `json:"nameservers"`
}

// DNSProvider writes the records Central publishes in its own zone: the A record of each appliance hostname,
// the temporary DNS-01 TXT records, and the zone's CAA policy. Every method acts on the configured zone only.
type DNSProvider interface {
	// UpsertA makes fqdn resolve to exactly ip (TTL 300, never proxied) and returns the record id.
	UpsertA(ctx context.Context, fqdn, ip string) (string, error)
	// DeleteRecord removes a record by id. A record that is already gone is not an error.
	DeleteRecord(ctx context.Context, recordID string) error
	// SetTXT adds a TXT record (TTL 60) and returns its id. Other TXT values at the same name are kept:
	// two orders may validate at the same time.
	SetTXT(ctx context.Context, fqdn, value string) (string, error)
	// ListAuthoritativeNS lists the zone's authoritative nameservers as host or host:port.
	ListAuthoritativeNS(ctx context.Context) ([]string, error)
	// ApplyCAA sets the apex `0 issue "<issueValue>"` and `0 issuewild ";"`, preserving every other record,
	// and returns the issue values of OTHER CAs it found and left in place (an operator should know).
	ApplyCAA(ctx context.Context, issueValue string) ([]string, error)
	// Verify proves the credentials reach the zone and returns its facts.
	Verify(ctx context.Context) (ZoneInfo, error)
}

// ---------------------------------------------------------------------------------------------------------
// challtestsrv (TEST-ONLY): Pebble's companion DNS server, driven through its management API, so the full
// issuance chain can be exercised -- in CI and on a real Central host -- with no DNS credential at all.
// ---------------------------------------------------------------------------------------------------------

// Challtestsrv drives pebble-challtestsrv's management API. challtestsrv answers for any name, so "the zone"
// is whatever name we write; its nameserver for propagation checks is CTRLAPI_PORTAL_DNS_CHECK_SERVERS.
type Challtestsrv struct {
	BaseURL     string   // management API, e.g. http://127.0.0.1:8055
	Zone        string   // the zone name settings say we manage
	NameServers []string // what ListAuthoritativeNS reports (host:port of challtestsrv's DNS listener)
	HTTP        *http.Client
}

func (c *Challtestsrv) post(ctx context.Context, path string, body any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("challtestsrv %s: %w", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("challtestsrv %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// challtestsrv keys records by (type, host) rather than issuing ids, so the id we hand out encodes both.
func ctsID(typ, fqdn string) string { return typ + "|" + dnsName(fqdn) }

func dnsName(fqdn string) string { return strings.TrimSuffix(fqdn, ".") + "." }

func (c *Challtestsrv) UpsertA(ctx context.Context, fqdn, ip string) (string, error) {
	_ = c.post(ctx, "/clear-a", map[string]any{"host": dnsName(fqdn)})
	if err := c.post(ctx, "/add-a", map[string]any{"host": dnsName(fqdn), "addresses": []string{ip}}); err != nil {
		return "", err
	}
	return ctsID("A", fqdn), nil
}

func (c *Challtestsrv) DeleteRecord(ctx context.Context, recordID string) error {
	typ, host, ok := strings.Cut(recordID, "|")
	if !ok {
		return fmt.Errorf("challtestsrv: unknown record id %q", recordID)
	}
	switch typ {
	case "A":
		return c.post(ctx, "/clear-a", map[string]any{"host": host})
	case "TXT":
		return c.post(ctx, "/clear-txt", map[string]any{"host": host})
	}
	return fmt.Errorf("challtestsrv: unknown record type %q", typ)
}

// SetTXT: challtestsrv holds one TXT value per name (set-txt replaces), which is enough for one validation
// at a time -- the worker processes orders one by one.
func (c *Challtestsrv) SetTXT(ctx context.Context, fqdn, value string) (string, error) {
	if err := c.post(ctx, "/set-txt", map[string]any{"host": dnsName(fqdn), "value": value}); err != nil {
		return "", err
	}
	return ctsID("TXT", fqdn), nil
}

func (c *Challtestsrv) ListAuthoritativeNS(context.Context) ([]string, error) {
	return c.NameServers, nil
}

func (c *Challtestsrv) ApplyCAA(ctx context.Context, issueValue string) ([]string, error) {
	err := c.post(ctx, "/add-caa", map[string]any{"host": dnsName(c.Zone), "policies": []map[string]string{
		{"tag": "issue", "value": issueValue}, {"tag": "issuewild", "value": ";"}}})
	return nil, err
}

func (c *Challtestsrv) Verify(ctx context.Context) (ZoneInfo, error) {
	// Any harmless management call proves the API answers.
	if err := c.post(ctx, "/clear-txt", map[string]any{"host": dnsName("_onegate-probe." + c.Zone)}); err != nil {
		return ZoneInfo{}, err
	}
	return ZoneInfo{ID: "challtestsrv", Name: c.Zone, NameServers: c.NameServers}, nil
}
