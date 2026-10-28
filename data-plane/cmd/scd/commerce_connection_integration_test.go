//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// P5 M2 step 2: when portald sends the connection's pair, a quote or confirm from a connection that is not the session's device is refused
// before the commerce engine runs.
func TestIntegration_CommerceRefusesAnotherConnection(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	var dev string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,$3::uuid,$4::macaddr) RETURNING id::text`, f.tenant, f.site, f.appliance, f.net.mac).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.checkConnection(ctx, connectionPair{IP: f.net.guestIP, MAC: f.net.mac}, dev, f.network); err != nil {
		t.Fatalf("the session's own connection was refused: %v", err)
	}
	if err := f.srv.checkConnection(ctx, connectionPair{}, dev, f.network); err != nil {
		t.Fatalf("a call without a pair (older portald) must proceed: %v", err)
	}
	for name, c := range map[string]connectionPair{
		"another device":    {IP: f.net.guestIP, MAC: "02:00:00:bb:00:01"},
		"another network":   {IP: "192.0.2.10", MAC: f.net.mac},
		"unparsable pair":   {IP: "x", MAC: "y"},
		"MAC without an IP": {MAC: f.net.mac},
	} {
		if err := f.srv.checkConnection(ctx, c, dev, f.network); !errors.Is(err, errConnectionMismatch) {
			t.Fatalf("%s: want errConnectionMismatch, got %v", name, err)
		}
	}
	// The handlers refuse before reaching the commerce engine (nil here: reaching it would panic).
	for path, body := range map[string]any{
		"quote":   map[string]string{"auth_context_id": "a", "device_id": dev, "guest_network_id": f.network, "package_id": "p", "ip": f.net.guestIP, "mac": "02:00:00:bb:00:01"},
		"confirm": map[string]string{"quote_id": "q", "device_id": dev, "guest_network_id": f.network, "ip": f.net.guestIP, "mac": "02:00:00:bb:00:01"},
	} {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/commerce/"+path, bytes.NewReader(b))
		if path == "quote" {
			f.srv.commerceQuote(rec, r)
		} else {
			f.srv.commerceConfirm(rec, r)
		}
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s from another device: status %d, want 409", path, rec.Code)
		}
	}
}
