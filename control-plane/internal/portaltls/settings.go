package portaltls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ACME directories (contract §3 "Staging first").
const (
	DirectoryStaging    = "staging"
	DirectoryProduction = "production"

	LetsEncryptStagingURL    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	LetsEncryptProductionURL = "https://acme-v02.api.letsencrypt.org/directory"
)

// DB is what this package needs from Postgres: a pool, a transaction or a connection all qualify.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Settings is the one portal_tls_settings row. The DNS token and ACME account key are files (Secrets), never
// columns: a database dump or a replica must not be enough to issue certificates for the zone.
type Settings struct {
	ACMEDirectory    string     `json:"acme_directory"`
	ContactEmail     string     `json:"contact_email"`
	ZoneName         string     `json:"zone_name"`
	ZoneID           string     `json:"zone_id"`
	AccountURL       string     `json:"account_url"`
	AccountDirectory string     `json:"account_directory"`
	WeeklyNewLimit   int        `json:"weekly_new_limit"`
	CAAAppliedAt     *time.Time `json:"caa_applied_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	UpdatedBy        string     `json:"updated_by"`
}

// LoadSettings reads the settings row (migration 0050 inserts it).
func LoadSettings(ctx context.Context, db DB) (Settings, error) {
	var s Settings
	err := db.QueryRow(ctx, `
        SELECT acme_directory, COALESCE(contact_email,''), zone_name, COALESCE(zone_id,''), COALESCE(account_url,''),
               COALESCE(account_directory,''), weekly_new_limit, caa_applied_at, updated_at, COALESCE(updated_by,'')
          FROM portal_tls_settings WHERE id`).
		Scan(&s.ACMEDirectory, &s.ContactEmail, &s.ZoneName, &s.ZoneID, &s.AccountURL, &s.AccountDirectory,
			&s.WeeklyNewLimit, &s.CAAAppliedAt, &s.UpdatedAt, &s.UpdatedBy)
	return s, err
}

// SettingsUpdate is what an operator may change. A nil field is left alone.
type SettingsUpdate struct {
	ACMEDirectory  *string `json:"acme_directory"`
	ContactEmail   *string `json:"contact_email"`
	ZoneName       *string `json:"zone_name"`
	WeeklyNewLimit *int    `json:"weekly_new_limit"`
}

// Validate checks an update against the same rules the table's CHECK constraints enforce, so the operator
// gets a sentence instead of a constraint name.
func (u SettingsUpdate) Validate() error {
	if u.ACMEDirectory != nil && *u.ACMEDirectory != DirectoryStaging && *u.ACMEDirectory != DirectoryProduction {
		return errors.New("acme_directory must be staging or production")
	}
	if u.ContactEmail != nil {
		e := strings.TrimSpace(*u.ContactEmail)
		at := strings.IndexByte(e, '@')
		if e != "" && (at < 1 || !strings.Contains(e[at+1:], ".") || strings.ContainsAny(e, " \t\r\n") || strings.Count(e, "@") != 1) {
			return errors.New("contact_email must be an email address")
		}
	}
	if u.ZoneName != nil {
		z := NormalizeFQDN(*u.ZoneName)
		if err := validHostnameSyntax(z); err != nil || !strings.Contains(z, ".") {
			return errors.New("zone_name must be a DNS zone such as onegate.work")
		}
	}
	if u.WeeklyNewLimit != nil && (*u.WeeklyNewLimit < 1 || *u.WeeklyNewLimit > 50) {
		return errors.New("weekly_new_limit must be between 1 and 50 (Let's Encrypt allows 50 per week)")
	}
	return nil
}

// SaveSettings applies an update. Changing the zone forgets the cached zone id (it belonged to the old zone);
// changing the directory leaves the account alone -- the next ACME call notices account_directory no longer
// matches and registers on the new directory.
func SaveSettings(ctx context.Context, db DB, u SettingsUpdate, actor string) (Settings, error) {
	if err := u.Validate(); err != nil {
		return Settings{}, err
	}
	var email, zone *string
	if u.ContactEmail != nil {
		e := strings.TrimSpace(*u.ContactEmail)
		email = &e
	}
	if u.ZoneName != nil {
		z := NormalizeFQDN(*u.ZoneName)
		zone = &z
	}
	if _, err := db.Exec(ctx, `
        UPDATE portal_tls_settings
           SET acme_directory   = COALESCE($1, acme_directory),
               contact_email    = CASE WHEN $2::text IS NULL THEN contact_email ELSE NULLIF($2,'') END,
               zone_id          = CASE WHEN $3::text IS NOT NULL AND $3 <> zone_name THEN NULL ELSE zone_id END,
               caa_applied_at   = CASE WHEN $3::text IS NOT NULL AND $3 <> zone_name THEN NULL ELSE caa_applied_at END,
               zone_name        = COALESCE($3, zone_name),
               weekly_new_limit = COALESCE($4, weekly_new_limit),
               updated_at = now(), updated_by = NULLIF($5,'')
         WHERE id`, u.ACMEDirectory, email, zone, u.WeeklyNewLimit, actor); err != nil {
		return Settings{}, err
	}
	return LoadSettings(ctx, db)
}

// ---------------------------------------------------------------------------------------------------------
// Secrets: two root-only files. Never columns, never logged, never returned by any API.
// ---------------------------------------------------------------------------------------------------------

const (
	accountKeyFile = "acme-account.key"
	dnsTokenFile   = "dns-api.token"
)

// Secrets is the directory holding the ACME account key and the DNS API token.
type Secrets struct{ Dir string }

func (s Secrets) path(name string) string { return filepath.Join(s.Dir, name) }

func (s Secrets) ensureDir() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.Dir, 0o700)
}

// TokenSet reports whether a DNS API token has been stored.
func (s Secrets) TokenSet() bool {
	fi, err := os.Stat(s.path(dnsTokenFile))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// ReadToken returns the stored DNS API token. Callers hand it straight to the provider; it is never logged.
func (s Secrets) ReadToken() (string, error) {
	raw, err := os.ReadFile(s.path(dnsTokenFile))
	if err != nil {
		return "", errors.New("no DNS API token is stored")
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", errors.New("no DNS API token is stored")
	}
	return t, nil
}

// WriteToken replaces the DNS API token atomically (temp file, fsync, rename): a crash leaves either the old
// token or the new one, never half of one.
func (s Secrets) WriteToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("the token must be one non-empty line without spaces")
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	return writeFileAtomic(s.path(dnsTokenFile), []byte(token+"\n"))
}

func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AccountKey loads the ACME account key, creating it on first use. Creation writes a complete temp file and
// then LINKS it into place: the link fails if another process created the key first, so two ctrlapi starts
// can never each register an account with a different key and leave the second one's account orphaned (the
// O_EXCL guarantee, without the half-written file a crash during an O_EXCL write would leave).
func (s Secrets) AccountKey() (*ecdsa.PrivateKey, error) {
	path := s.path(accountKeyFile)
	if k, err := readECKey(path); err == nil {
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.Dir, "."+accountKeyFile+".tmp-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_ = f.Chmod(0o600)
	if err := pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readECKey(path) // somebody else won; use theirs
		}
		return nil, err
	}
	return key, nil
}

func readECKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM key", filepath.Base(path))
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s does not parse as an EC key", filepath.Base(path))
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an EC key", filepath.Base(path))
	}
	return ec, nil
}
