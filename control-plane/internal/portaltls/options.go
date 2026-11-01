package portaltls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	ProviderCloudflare   = "cloudflare"
	ProviderChalltestsrv = "challtestsrv"
)

// DefaultDoHURLs are two independent resolvers that must agree before a hotel domain is VERIFIED.
var DefaultDoHURLs = []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"}

// Options holds where the secrets live plus test-only overrides, all off by default; OptionsFromEnv
// reports the active ones so ctrlapi can log them at startup.
type Options struct {
	SecretsDir string

	// Test-only overrides.
	DNSProvider      string        // cloudflare (default) | challtestsrv
	ChalltestsrvURL  string        // management API base
	DirectoryURL     string        // overrides the staging/production ACME directory URL
	ACMECAFile       string        // extra root (PEM) trusted for the ACME connection only
	DNSCheckServers  []string      // host:port list replacing the zone's authoritative nameservers
	DoHURLs          []string      // DNS-over-HTTPS JSON endpoints for hotel-domain checks
	CloudflareAPI    string        // not read from the environment
	PropagationLimit time.Duration // default 2 min
	PropagationPoll  time.Duration // default 5 s

	// The shared Client Portal name (shared.go). Not a test override: the defaults are the product's.
	// CTRLAPI_PORTAL_SHARED_HOSTNAME=off switches the shared name off.
	SharedHostname string
	SharedAddress  string
}

// OptionsFromEnv reads CTRLAPI_PORTAL_* and lists the active test overrides (empty in production).
func OptionsFromEnv() (Options, []string) {
	o := Options{
		SecretsDir:      envOr("CTRLAPI_PORTAL_TLS_DIR", "/etc/stayconnect/portal-tls"),
		DNSProvider:     strings.ToLower(strings.TrimSpace(envOr("CTRLAPI_PORTAL_DNS_PROVIDER", ProviderCloudflare))),
		ChalltestsrvURL: strings.TrimRight(strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_CHALLTESTSRV_URL")), "/"),
		DirectoryURL:    strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_ACME_DIRECTORY_URL")),
		ACMECAFile:      strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_ACME_CA_FILE")),
		DNSCheckServers: splitList(os.Getenv("CTRLAPI_PORTAL_DNS_CHECK_SERVERS")),
		DoHURLs:         splitList(os.Getenv("CTRLAPI_PORTAL_DOH_URLS")),
		SharedHostname:  NormalizeFQDN(envOr("CTRLAPI_PORTAL_SHARED_HOSTNAME", DefaultSharedHostname)),
		SharedAddress:   envOr("CTRLAPI_PORTAL_SHARED_ADDRESS", DefaultSharedAddress),
	}
	var active []string
	if o.DNSProvider != ProviderCloudflare {
		active = append(active, "CTRLAPI_PORTAL_DNS_PROVIDER="+o.DNSProvider)
	}
	if o.ChalltestsrvURL != "" {
		active = append(active, "CTRLAPI_PORTAL_CHALLTESTSRV_URL")
	}
	if o.DirectoryURL != "" {
		active = append(active, "CTRLAPI_PORTAL_ACME_DIRECTORY_URL="+o.DirectoryURL)
	}
	if o.ACMECAFile != "" {
		active = append(active, "CTRLAPI_PORTAL_ACME_CA_FILE")
	}
	if len(o.DNSCheckServers) > 0 {
		active = append(active, "CTRLAPI_PORTAL_DNS_CHECK_SERVERS="+strings.Join(o.DNSCheckServers, ","))
	}
	if len(o.DoHURLs) > 0 {
		active = append(active, "CTRLAPI_PORTAL_DOH_URLS")
	}
	return o, active
}

func (o Options) Check() error {
	switch o.DNSProvider {
	case ProviderCloudflare:
	case ProviderChalltestsrv:
		if o.ChalltestsrvURL == "" {
			return errors.New("CTRLAPI_PORTAL_DNS_PROVIDER=challtestsrv needs CTRLAPI_PORTAL_CHALLTESTSRV_URL")
		}
	default:
		return fmt.Errorf("CTRLAPI_PORTAL_DNS_PROVIDER must be cloudflare or challtestsrv, not %q", o.DNSProvider)
	}
	for _, s := range o.DNSCheckServers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			return fmt.Errorf("CTRLAPI_PORTAL_DNS_CHECK_SERVERS entry %q must be host:port", s)
		}
	}
	if h := o.SharedHostname; h != "" && h != "off" {
		if err := validHostnameSyntax(h); err != nil || strings.Count(h, ".") < 2 {
			return fmt.Errorf("CTRLAPI_PORTAL_SHARED_HOSTNAME %q must be a host name such as portal.example.com", h)
		}
		if _, err := ParsePortalIP(o.SharedAddress); err != nil {
			return fmt.Errorf("CTRLAPI_PORTAL_SHARED_ADDRESS: %v", err)
		}
	}
	return nil
}

// Shared is the configured shared Client Portal name; disabled when switched off or unset (as in unit tests).
func (o Options) Shared() SharedPortal {
	h := NormalizeFQDN(o.SharedHostname)
	if h == "" || h == "off" {
		return SharedPortal{}
	}
	a, err := ParsePortalIP(o.SharedAddress)
	if err != nil {
		return SharedPortal{}
	}
	return SharedPortal{Hostname: h, Address: a}
}

// CheckEnv is Check plus the rule that a production Central never uses the test DNS server (audit Phase 1 CC2).
func (o Options) CheckEnv(production bool) error {
	if err := o.Check(); err != nil {
		return err
	}
	if production && o.DNSProvider == ProviderChalltestsrv {
		return errors.New("CTRLAPI_PORTAL_DNS_PROVIDER=challtestsrv is a test DNS server and is refused when CTRLAPI_ENV=production")
	}
	return nil
}

func (o Options) dohURLs() []string {
	if len(o.DoHURLs) > 0 {
		return o.DoHURLs
	}
	return DefaultDoHURLs
}

func (o Options) propagationLimit() time.Duration {
	if o.PropagationLimit > 0 {
		return o.PropagationLimit
	}
	return 2 * time.Minute
}

func (o Options) propagationPoll() time.Duration {
	if o.PropagationPoll > 0 {
		return o.PropagationPoll
	}
	return 5 * time.Second
}

func (o Options) directoryURL(setting string) string {
	if o.DirectoryURL != "" {
		return o.DirectoryURL
	}
	if setting == DirectoryProduction {
		return LetsEncryptProductionURL
	}
	return LetsEncryptStagingURL
}

// acmeHTTPClient: ACMECAFile adds a root for this connection only; system roots stay trusted.
func (o Options) acmeHTTPClient() (*http.Client, error) {
	if o.ACMECAFile == "" {
		return &http.Client{Timeout: 60 * time.Second}, nil
	}
	pemBytes, err := os.ReadFile(o.ACMECAFile)
	if err != nil {
		return nil, fmt.Errorf("CTRLAPI_PORTAL_ACME_CA_FILE: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("CTRLAPI_PORTAL_ACME_CA_FILE holds no PEM certificate")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: 60 * time.Second, Transport: tr}, nil
}

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
