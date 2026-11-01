package portaltls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// THE SHARED CLIENT PORTAL NAME (Product Owner decision, 2026-11-01). Every appliance's guests reach their own
// appliance's portal at one permanent public name. The name resolves in public DNS to one fleet-wide private
// portal address, which exists only on each appliance's ogportal0 and is never routed: a guest's packets for it
// reach the guest's own default gateway, which is that hotel's appliance. Central never carries guest traffic.
//
// Each appliance holds its OWN key and its own certificate for the shared name. The certificate also names the
// appliance's permanent <label>.gw.<zone> hostname, so every appliance's identifier set is unique: the CA's
// limit on certificates per exact set of names never couples two appliances, and each one can be revoked alone.
// Central answers the DNS-01 challenge for the shared name: directly at its _acme-challenge name when the name lies
// under Central's name base (the default), otherwise at one delegate, <SharedChallengeLabel>.acme.<zone>, which the
// shared name's _acme-challenge CNAME points at.
const (
	DefaultSharedHostname = "portal.onegate.getsemantics.com"
	DefaultSharedAddress  = "10.255.255.1"
	// SharedChallengeLabel is 12 characters: a hotel delegate label is 16, so the two can never collide.
	SharedChallengeLabel = "sharedportal"

	sharedCheckOK  = 10 * time.Minute // a passing public-DNS check is trusted this long
	sharedCheckBad = time.Minute      // a failing one is retried no sooner than this
	rrA            = 1
)

// SharedPortal is the configured shared name; Hostname "" means the feature is switched off.
type SharedPortal struct {
	Hostname string
	Address  netip.Addr
}

func (sp SharedPortal) Enabled() bool { return sp.Hostname != "" && sp.Address.IsValid() }

// InZone reports whether the shared name lies under Central's name base, so Central writes its challenge
// directly and no delegation is needed (the default: portal.onegate.getsemantics.com under onegate.getsemantics.com).
func (sp SharedPortal) InZone(zone string) bool {
	z := NormalizeFQDN(zone)
	return sp.Enabled() && z != "" && strings.HasSuffix(sp.Hostname, "."+z)
}

// SharedChallengeName is the TXT name Central writes for the shared name's DNS-01 challenges: the name's own
// _acme-challenge inside the base, otherwise the delegate the name's _acme-challenge CNAME points at.
func SharedChallengeName(sp SharedPortal, zone string) string {
	if sp.InZone(zone) {
		return "_acme-challenge." + sp.Hostname
	}
	return SharedChallengeLabel + "." + ChallengeSubdomain + "." + NormalizeFQDN(zone)
}

// SharedRecords are the public records the shared name needs, published once for the whole fleet: its A record,
// and the challenge delegation only when the name lies outside the base.
func SharedRecords(sp SharedPortal, zone string) []Record {
	if !sp.Enabled() {
		return []Record{}
	}
	out := []Record{{Type: "A", Name: sp.Hostname, Value: sp.Address.String()}}
	if !sp.InZone(zone) {
		out = append(out, Record{Type: "CNAME", Name: "_acme-challenge." + sp.Hostname, Value: SharedChallengeName(sp, zone) + "."})
	}
	return out
}

// SharedView is what an appliance (in its sync answer) and an operator learn about the shared name.
type SharedView struct {
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	// Eligible: this appliance's portal address IS the shared address, so the public name reaches its portal.
	// An appliance on another address keeps its own hostname (the public answer would not reach it).
	Eligible bool `json:"eligible"`
	// Ready: public DNS publishes the name at the shared address (and delegates its challenge, when outside the base).
	Ready   bool     `json:"ready"`
	Error   *string  `json:"error"`
	Records []Record `json:"records"`
}

// IsSharedName reports whether fqdn is the shared name or below it (never a hotel's own domain).
func (sp SharedPortal) IsSharedName(fqdn string) bool {
	if !sp.Enabled() {
		return false
	}
	f := NormalizeFQDN(fqdn)
	return f == sp.Hostname || strings.HasSuffix(f, "."+sp.Hostname)
}

// CheckShared runs the public-DNS checks for the shared name through one resolver: the name's A records are
// exactly the shared address (any other answer would send some guests elsewhere), its _acme-challenge is the
// CNAME to Central's delegate, and CAA lets Let's Encrypt issue.
func CheckShared(ctx context.Context, r DoHResolver, sp SharedPortal, zone, accountURL string) error {
	got, err := r.Query(ctx, sp.Hostname, rrA)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return fmt.Errorf("A %s is not published yet (it must be %s)", sp.Hostname, sp.Address)
	}
	for _, g := range got {
		if strings.TrimSpace(g) != sp.Address.String() {
			return fmt.Errorf("A %s answers %s; it must answer only %s", sp.Hostname, strings.Join(got, ", "), sp.Address)
		}
	}
	if !sp.InZone(zone) {
		if err := checkCNAME(ctx, r, "_acme-challenge."+sp.Hostname, SharedChallengeName(sp, zone)); err != nil {
			return err
		}
	}
	return checkCAA(ctx, r, sp.Hostname, accountURL)
}

// checkCAA climbs to the registrable domain; the first name with any CAA record decides (RFC 8659 §3).
func checkCAA(ctx context.Context, r DoHResolver, fqdn, accountURL string) error {
	reg, err := RegistrableDomain(fqdn)
	if err != nil {
		return err
	}
	for name := fqdn; ; {
		raw, err := r.Query(ctx, name, rrCAA)
		if err != nil {
			return err
		}
		if len(raw) > 0 {
			set := make([]CAARecord, 0, len(raw))
			for _, d := range raw {
				rec, err := ParseCAA(d)
				if err != nil {
					return fmt.Errorf("CAA at %s: %v", name, err)
				}
				set = append(set, rec)
			}
			if ok, why := CAAPermits(set, accountURL); !ok {
				return fmt.Errorf("CAA at %s: %s", name, why)
			}
			return nil
		}
		if name == reg {
			return nil
		}
		_, parent, _ := strings.Cut(name, ".")
		name = parent
	}
}

// CheckSharedEverywhere needs every resolver to pass, as for a hotel domain.
func CheckSharedEverywhere(ctx context.Context, urls []string, hc *http.Client, sp SharedPortal, zone, accountURL string) error {
	ch := make(chan error, len(urls))
	for _, u := range urls {
		go func(u string) { ch <- CheckShared(ctx, DoHResolver{URL: u, HTTP: hc}, sp, zone, accountURL) }(u)
	}
	var first error
	for range urls {
		if err := <-ch; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// sharedCache keeps the last public-DNS verdict per (name, address, zone): the check costs a few DoH queries
// and every appliance's sync asks for it.
type sharedCache struct {
	mu        sync.Mutex
	key       string
	checkedAt time.Time
	err       error
}

// sharedReady answers whether public DNS is ready for the shared name. force re-checks now (before an order).
func (s *Service) sharedReady(ctx context.Context, st Settings, force bool) (bool, string) {
	sp := s.Opts.Shared()
	if !sp.Enabled() {
		return false, "the shared portal name is switched off"
	}
	key := sp.Hostname + "|" + sp.Address.String() + "|" + st.ZoneName + "|" + st.AccountURL
	c := &s.shared
	c.mu.Lock()
	fresh := c.key == key && !c.checkedAt.IsZero() &&
		((c.err == nil && time.Since(c.checkedAt) < sharedCheckOK) || (c.err != nil && time.Since(c.checkedAt) < sharedCheckBad))
	if fresh && !force {
		err := c.err
		c.mu.Unlock()
		return err == nil, errText(err)
	}
	c.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := CheckSharedEverywhere(cctx, s.Opts.dohURLs(), s.HTTP, sp, st.ZoneName, st.AccountURL)
	c.mu.Lock()
	prevOK := c.key == key && !c.checkedAt.IsZero() && c.err == nil
	c.key, c.checkedAt, c.err = key, time.Now(), err
	c.mu.Unlock()
	if err == nil && !prevOK {
		AppendEvent(ctx, s.DB, "", sp.Hostname, EvSharedReady, ActorSystem, map[string]any{"address": sp.Address.String()})
	}
	if err != nil && prevOK {
		AppendEvent(ctx, s.DB, "", sp.Hostname, EvSharedNotReady, ActorSystem, map[string]any{"error": truncate(err.Error(), 500)})
	}
	return err == nil, errText(err)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 500)
}

// sharedView describes the shared name for one appliance (portalIP "" = an operator's view).
func (s *Service) sharedView(ctx context.Context, st Settings, portalIP string) *SharedView {
	sp := s.Opts.Shared()
	if !sp.Enabled() {
		return nil
	}
	v := &SharedView{Hostname: sp.Hostname, Address: sp.Address.String(), Records: SharedRecords(sp, st.ZoneName)}
	v.Eligible = portalIP != "" && portalIP == sp.Address.String()
	// The check is made only when it matters to the caller: an operator, or an appliance that can use the name.
	if portalIP == "" || v.Eligible {
		ok, why := s.sharedReady(ctx, st, false)
		v.Ready = ok
		if why != "" {
			v.Error = &why
		}
	}
	if portalIP != "" && !v.Eligible {
		why := fmt.Sprintf("this appliance's portal address is not %s; it keeps its own portal hostname", sp.Address)
		v.Error = &why
	}
	return v
}

// ParseAndCheckSharedCSR validates the CSR for a shared-name certificate: the same rules as ParseAndCheckCSR,
// except that it names exactly two DNS names, the shared name and the appliance's own OneGate hostname, and its
// common name is empty or one of them.
func ParseAndCheckSharedCSR(csrPEM, shared, own string) (*x509.CertificateRequest, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("csr_pem must be one PEM CERTIFICATE REQUEST")
	}
	if len(rest) > 0 {
		if b2, _ := pem.Decode(rest); b2 != nil {
			return nil, errors.New("csr_pem must contain exactly one PEM block")
		}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the CSR does not parse: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, errors.New("the CSR signature does not verify")
	}
	if len(csr.IPAddresses) > 0 || len(csr.EmailAddresses) > 0 || len(csr.URIs) > 0 {
		return nil, errors.New("the CSR may name only DNS names (no IP, email or URI names)")
	}
	want := []string{shared, own}
	got := append([]string{}, csr.DNSNames...)
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		return nil, fmt.Errorf("a shared-portal CSR must name exactly %s and %s, it names %v", shared, own, csr.DNSNames)
	}
	if cn := csr.Subject.CommonName; cn != "" && cn != shared && cn != own {
		return nil, errors.New("the CSR's common name must be empty or one of its DNS names")
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, errors.New("an ECDSA CSR key must be P-256")
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return nil, errors.New("an RSA CSR key must be at least 2048 bits")
		}
	default:
		return nil, errors.New("the CSR key must be ECDSA P-256 or RSA (2048 bits or more)")
	}
	return csr, nil
}
