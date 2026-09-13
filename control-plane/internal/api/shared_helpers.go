package api

// Helpers that OUTLIVED the removal of Central's non-licensing surfaces.
//
// PMSProvider, strDeref and newUUIDv4 happened to be declared inside pms_admin.go and commands_api.go --
// files that existed to let Central reach into a hotel. The reaching-in is gone; these are not part of it.
// PMSProvider is still the shape the appliance registry reports a tenant's configured provider as, and the
// other two are three-line utilities used across the package.
//
// Deleting a shared declaration because the file around it was deleted is how a removal turns into an
// outage, so they are moved here rather than dropped.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

type PMSProvider struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	SiteID        string          `json:"site_id,omitempty"` // empty → tenant-wide
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Enabled       bool            `json:"enabled"`
	DisplayName   string          `json:"display_name,omitempty"`
	Host          string          `json:"host,omitempty"`
	Port          int             `json:"port,omitempty"`
	UseTLS        bool            `json:"use_tls"`
	BaseURL       string          `json:"base_url,omitempty"`
	PropertyID    string          `json:"property_id,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	FieldMap      json.RawMessage `json:"field_map,omitempty"`
	Normalization json.RawMessage `json:"normalization,omitempty"`
	StayWindow    json.RawMessage `json:"stay_window,omitempty"`
	Status        string          `json:"status"`
	LastRecordAt  *time.Time      `json:"last_record_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	LastErrorAt   *time.Time      `json:"last_error_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16])
}
