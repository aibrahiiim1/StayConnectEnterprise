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

// DNS provider kinds (CTRLAPI_PORTAL_DNS_PROVIDER).
const (
	ProviderCloudflare   = "cloudflare"
	ProviderChalltestsrv = "challtestsrv"
)

// Default DNS-over-HTTPS resolvers for hotel-domain checks (contract §3.1): two independent operators, both
// of which must agree before a domain is VERIFIED.
var DefaultDoHURLs = []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"}

// Options is how this process obtains certificates: where the secrets live, and the TEST-ONLY overrides that
// let the whole chain run against Pebble and challtestsrv before a real DNS token exists. Every override
// defaults to off; OptionsFromEnv reports the ones that are on so ctrlapi can say so at startup.
type Options struct {
	SecretsDir string

	// TEST-ONLY overrides (deploy/env/ctrlapi.env.example).
	DNSProvider      string        // cloudflare (default) | challtestsrv
	ChalltestsrvURL  string        // challtestsrv management API base, e.g. http://127.0.0.1:8055
	DirectoryURL     string        // overrides the staging/production ACME directory URL
	ACMECAFile       string        // extra root (PEM) trusted for the HTTPS connection to the ACME server only
	DNSCheckServers  []string      // host:port list replacing the zone's authoritative nameservers
	DoHURLs          []string      // DNS-over-HTTPS JSON endpoints for hotel-domain checks
	CloudflareAPI    string        // Cloudflare API base (tests only; not read from the environment)
	PropagationLimit time.Duration // how long to wait for a TXT record at every nameserver (default 2 min)
	PropagationPoll  time.Duration // and how often to look (default 5 s)
}

// OptionsFromEnv reads CTRLAPI_PORTAL_* and returns the options plus a human list of the test overrides that
// are active (empty in production).
func OptionsFromEnv() (Options, []string) {
	o := Options{
		SecretsDir:      envOr("CTRLAPI_PORTAL_TLS_DIR", "/etc/stayconnect/portal-tls"),
		DNSProvider:     strings.ToLower(strings.TrimSpace(envOr("CTRLAPI_PORTAL_DNS_PROVIDER", ProviderCloudflare))),
		ChalltestsrvURL: strings.TrimRight(strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_CHALLTESTSRV_URL")), "/"),
		DirectoryURL:    strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_ACME_DIRECTORY_URL")),
		ACMECAFile:      strings.TrimSpace(os.Getenv("CTRLAPI_PORTAL_ACME_CA_FILE")),
		DNSCheckServers: splitList(os.Getenv("CTRLAPI_PORTAL_DNS_CHECK_SERVERS")),
		DoHURLs:         splitList(os.Getenv("CTRLAPI_PORTAL_DOH_URLS")),
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

// Check refuses an inconsistent set of overrides at startup rather than at the first order.
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

// directoryURL is the ACME directory for a settings value, unless the test override replaces it.
func (o Options) directoryURL(setting string) string {
	if o.DirectoryURL != "" {
		return o.DirectoryURL
	}
	if setting == DirectoryProduction {
		return LetsEncryptProductionURL
	}
	return LetsEncryptStagingURL
}

// acmeHTTPClient is the client for the ACME server. ACMECAFile ADDS a root for this one connection (Pebble's
// test CA); the system roots stay trusted and nothing else in the process is affected.
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
