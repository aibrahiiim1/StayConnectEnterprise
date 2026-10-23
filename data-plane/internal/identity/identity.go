// Package identity manages the appliance's persistent Ed25519 identity and its token-less registration with Central.
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
)

// Identity is the appliance's resolved identity. TenantID/SiteID are legacy and never read for authority: the signed assignment is the only source of tenant and site.
type Identity struct {
	ApplianceID  string `json:"appliance_id"`
	TenantID     string `json:"tenant_id"`
	SiteID       string `json:"site_id"`
	Serial       string `json:"serial"`
	PublicKeyB64 string `json:"public_key"`
	privKey      ed25519.PrivateKey
}

func (i *Identity) PrivateKey() ed25519.PrivateKey { return i.privKey }

// Store is the on-disk identity directory (identity.json and ed25519.key).
type Store struct {
	Dir string
	// HeldCustomer reports the customer id whose data the local site database still holds ("" when clean). Register
	// sends it signed so Central refuses activation for any other customer; an error refuses registration.
	HeldCustomer func(context.Context) (string, error)
}

func (s *Store) idPath() string  { return filepath.Join(s.Dir, "identity.json") }
func (s *Store) keyPath() string { return filepath.Join(s.Dir, "ed25519.key") }

// ErrCentralUnreachable marks a registration that got no HTTP answer from Central; it is retryable.
var ErrCentralUnreachable = errors.New("central unreachable")

// LoadBound returns the persisted identity only when Central has bound an appliance id to it.
// An unbound keypair (offline first activation) is not an enrolled identity; counting it would stop the
// appliance from ever self-registering online. Returns (nil, nil) when not yet registered.
func (s *Store) LoadBound() (*Identity, error) {
	id, err := s.load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("identity load: %w", err)
	}
	if id.ApplianceID == "" {
		return nil, nil
	}
	return id, nil
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Register performs token-less self-registration, signed with the identity key (proof of possession).
// An existing unbound keypair is reused and a new key is written to disk before the request, so every retry
// presents the same key. Network failures wrap ErrCentralUnreachable; refusals return *StatusError.
func (s *Store) Register(ctx context.Context, ctrlBase string, client HTTPDoer) (*Identity, error) {
	if ctrlBase == "" {
		return nil, errors.New("identity: no Central endpoint configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if id, err := s.LoadBound(); err != nil {
		return nil, err
	} else if id != nil {
		return id, nil
	}
	local, err := s.EnsureLocalKeypair()
	if err != nil {
		return nil, err
	}
	priv := local.privKey
	pub := priv.Public().(ed25519.PublicKey)
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	hw := detectHW()
	fields := map[string]string{
		"serial":               hw.Serial,
		"wan_mac":              hw.WANMAC,
		"lan_mac":              hw.LANMAC,
		"hardware_fingerprint": hw.Fingerprint,
		"hostname":             hw.Hostname,
		"model":                hw.Model,
		"public_key":           pubB64,
	}
	// HOLDS_CUSTOMER_ID is inside the signed body (the token carries its SHA-256) so it cannot be stripped in transit.
	if s.HeldCustomer != nil {
		held, err := s.HeldCustomer(ctx)
		if err != nil {
			return nil, fmt.Errorf("registration refused locally: %w", err)
		}
		if held != "" {
			fields["holds_customer_id"] = held
		}
	}
	body, _ := json.Marshal(fields)
	kid := applianceauth.KeyID(pub)
	tok, err := applianceauth.SignRequest(priv, kid, http.MethodPost, "/v1/appliances/register", body)
	if err != nil {
		return nil, fmt.Errorf("sign register: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, ctrlBase+"/v1/appliances/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := client.Do(req)
	if err != nil {
		// Not fatal but never silent: the usual cause is that the appliance does not trust Central's CA.
		slog.Warn("appliance self-registration could not reach Central; will retry",
			"endpoint", ctrlBase+"/v1/appliances/register", "err", err,
			"hint", "TLS verification failure? the appliance must trust Central's CA — "+
				"deploy/scripts/install-central-trust.sh")
		return nil, fmt.Errorf("%w: %v", ErrCentralUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		return nil, &StatusError{Code: resp.StatusCode, Body: string(b[:n])}
	}
	var r struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("register decode: %w", err)
	}
	if r.ApplianceID == "" {
		return nil, errors.New("register: Central returned no appliance id")
	}
	// Tenant and site never come from the registration answer; only the signed assignment is authority.
	if local.Serial == "" {
		local.Serial = hw.Serial
	}
	local.ApplianceID = r.ApplianceID
	js, _ := json.MarshalIndent(local, "", "  ")
	if err := writeFileAtomic(s.idPath(), js, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return local, nil
}

type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("register status=%d body=%s", e.Code, e.Body)
}

var detectHW = hwid.Detect

// writeFileAtomic fsyncs a temp file and renames it so readers never see a partial file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *Store) load() (*Identity, error) {
	ij, err := os.ReadFile(s.idPath())
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(ij, &id); err != nil {
		return nil, fmt.Errorf("parse identity.json: %w", err)
	}
	priv, err := os.ReadFile(s.keyPath())
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519.key wrong size: %d", len(priv))
	}
	id.privKey = ed25519.PrivateKey(priv)
	return &id, nil
}

// LoadPublic returns the public identity without reading the private key. pmsd uses it: it never signs, and
// ed25519.key is root-only 0600 while identity.json is world-readable.
func (s *Store) LoadPublic() (*Identity, error) {
	ij, err := os.ReadFile(s.idPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(ij, &id); err != nil {
		return nil, fmt.Errorf("parse identity.json: %w", err)
	}
	return &id, nil
}

// EnsureLocalKeypair creates an identity keypair without contacting Central (offline first activation).
// ApplianceID stays empty: only Central mints ids, and only the signed assignment carries tenant and site.
func (s *Store) EnsureLocalKeypair() (*Identity, error) {
	if id, err := s.load(); err == nil {
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("identity load: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", s.Dir, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	hw := detectHW()
	id := &Identity{Serial: hw.Serial, PublicKeyB64: base64.RawStdEncoding.EncodeToString(pub), privKey: priv}
	// Key first (0600), then identity.json, so a crash between the two leaves a key with no claim rather
	// than a claim with no key.
	if err := writeFileAtomic(s.keyPath(), priv, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	b, _ := json.Marshal(id)
	if err := writeFileAtomic(s.idPath(), b, 0o644); err != nil {
		return nil, fmt.Errorf("write identity.json: %w", err)
	}
	return id, nil
}

// AdoptApplianceID records the id Central minted for an offline activation; it refuses to overwrite a different id.
func (s *Store) AdoptApplianceID(applianceID string) error {
	id, err := s.load()
	if err != nil {
		return err
	}
	if id.ApplianceID != "" && id.ApplianceID != applianceID {
		return fmt.Errorf("identity already bound to appliance %s", id.ApplianceID)
	}
	if id.ApplianceID == applianceID {
		return nil
	}
	id.ApplianceID = applianceID
	b, _ := json.Marshal(id)
	return writeFileAtomic(s.idPath(), b, 0o644)
}
