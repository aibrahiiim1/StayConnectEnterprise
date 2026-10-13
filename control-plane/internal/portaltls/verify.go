package portaltls

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HOTEL DOMAIN VERIFICATION (contract §3.1). A hotel proves wifi.nilehotel.com with three public records:
//
//	_onegate-verify.<fqdn>  TXT    <verify_token>                        the claimant controls the name
//	<fqdn>                  CNAME  <label>.gw.onegate.work.              the name follows the portal address
//	_acme-challenge.<fqdn>  CNAME  <challenge_label>.acme.onegate.work.  Central may answer DNS-01 for it
//
// plus a CAA policy (on the name or the nearest parent that has one) that lets Let's Encrypt issue. Each is
// read through two independent public DNS-over-HTTPS resolvers, and VERIFIED needs both to agree: one
// resolver with a stale or poisoned cache must not be enough to bind a name, nor to break one.

// DoH answer record types.
const (
	rrCNAME = 5
	rrTXT   = 16
	rrCAA   = 257
)

type dohAnswer struct {
	Name string `json:"name"`
	Type int    `json:"type"`
	Data string `json:"data"`
}

type dohResponse struct {
	Status int         `json:"Status"`
	Answer []dohAnswer `json:"Answer"`
}

// DoHResolver queries one DNS-over-HTTPS JSON endpoint (Cloudflare's /dns-query and Google's /resolve speak
// the same JSON dialect).
type DoHResolver struct {
	URL  string
	HTTP *http.Client
}

// Query returns the answer records of type typ for name. Records of other types, such as the CNAMEs a
// resolver followed, are dropped; records of the asked type at the END of a CNAME chain are kept, because
// that is the RRset a CA sees for the name too (RFC 8659 §3). A CNAME query keeps only the name's own CNAME.
// NXDOMAIN is an empty answer, not an error.
func (d DoHResolver) Query(ctx context.Context, name string, typ int) ([]string, error) {
	u, err := url.Parse(d.URL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("type", strconv.Itoa(typ))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u.Host, resp.StatusCode)
	}
	var r dohResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer", u.Host)
	}
	switch r.Status {
	case 0, 3: // NOERROR, NXDOMAIN
	default:
		return nil, fmt.Errorf("%s: DNS status %d for %s", u.Host, r.Status, name)
	}
	var out []string
	for _, a := range r.Answer {
		if a.Type != typ {
			continue
		}
		if typ != rrCNAME || strings.EqualFold(strings.TrimSuffix(a.Name, "."), strings.TrimSuffix(name, ".")) {
			out = append(out, a.Data)
		}
	}
	return out, nil
}

// txtValue undoes presentation quoting: `"abc" "def"` -> abcdef.
func txtValue(data string) string {
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, `"`) {
		return data
	}
	var b strings.Builder
	in := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '\\' && in && i+1 < len(data):
			i++
			b.WriteByte(data[i])
		case c == '"':
			in = !in
		case in:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// CAARecord is one CAA record.
type CAARecord struct {
	Flags int
	Tag   string
	Value string
}

// ParseCAA reads a CAA record in presentation form (`0 issue "letsencrypt.org"`) or in the RFC 3597
// generic form some resolvers return for it (`\# 22 00 05 69 73 73 75 65 ...`).
func ParseCAA(data string) (CAARecord, error) {
	data = strings.TrimSpace(data)
	if strings.HasPrefix(data, `\#`) {
		f := strings.Fields(data)
		if len(f) < 2 {
			return CAARecord{}, errors.New("bad generic CAA record")
		}
		raw, err := hex.DecodeString(strings.Join(f[2:], ""))
		if err != nil || len(raw) < 2 || len(raw) < 2+int(raw[1]) {
			return CAARecord{}, errors.New("bad generic CAA record")
		}
		tl := int(raw[1])
		return CAARecord{Flags: int(raw[0]), Tag: strings.ToLower(string(raw[2 : 2+tl])), Value: string(raw[2+tl:])}, nil
	}
	f := strings.SplitN(data, " ", 3)
	if len(f) != 3 {
		return CAARecord{}, fmt.Errorf("bad CAA record %q", data)
	}
	flags, err := strconv.Atoi(f[0])
	if err != nil {
		return CAARecord{}, fmt.Errorf("bad CAA flags %q", f[0])
	}
	return CAARecord{Flags: flags, Tag: strings.ToLower(f[1]), Value: txtValue(f[2])}, nil
}

// caaIssuerDomain is the issuer-domain-name of an issue value: "letsencrypt.org; accounturi=x" -> letsencrypt.org.
func caaIssuerDomain(v string) string {
	d, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(d))
}

func caaParams(v string) map[string]string {
	out := map[string]string{}
	_, rest, ok := strings.Cut(v, ";")
	if !ok {
		return out
	}
	for _, p := range strings.Split(rest, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok {
			out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(val)
		}
	}
	return out
}

// CAAPermits applies RFC 8659 to the CAA RRset of the relevant name: may Let's Encrypt issue a (non-wildcard)
// certificate, by DNS-01, for our account? An empty set permits everything. A critical flag on a tag we do not
// understand forbids issuance (that is what the flag is for). With no "issue" record the set does not restrict
// non-wildcard issuance. Otherwise one issue record must name letsencrypt.org and, if it pins an account or
// validation methods, include ours (accountURL "" = we do not know ours yet, so a pin cannot be judged here).
func CAAPermits(set []CAARecord, accountURL string) (bool, string) {
	if len(set) == 0 {
		return true, ""
	}
	var issues []CAARecord
	for _, r := range set {
		switch r.Tag {
		case "issue":
			issues = append(issues, r)
		case "issuewild", "iodef", "contactemail", "contactphone", "issuemail", "issuevmc":
		default:
			if r.Flags&128 != 0 {
				return false, fmt.Sprintf("a critical CAA record (%s) that no CA can honour forbids issuance", r.Tag)
			}
		}
	}
	if len(issues) == 0 {
		return true, ""
	}
	for _, r := range issues {
		if caaIssuerDomain(r.Value) != "letsencrypt.org" {
			continue
		}
		p := caaParams(r.Value)
		if acc, ok := p["accounturi"]; ok && accountURL != "" && acc != accountURL {
			continue
		}
		if vm, ok := p["validationmethods"]; ok {
			okVM := false
			for _, m := range strings.Split(vm, ",") {
				if strings.TrimSpace(m) == "dns-01" {
					okVM = true
				}
			}
			if !okVM {
				continue
			}
		}
		return true, ""
	}
	return false, "the domain's CAA records do not allow Let's Encrypt to issue for it; add 0 issue \"letsencrypt.org\""
}

// DomainCheck is what a hotel domain must show (from the claim row and the appliance's hostname).
type DomainCheck struct {
	FQDN           string
	VerifyToken    string
	CNAMETarget    string // <label>.gw.onegate.work
	ChallengeAlias string // <challenge_label>.acme.onegate.work
	AccountURL     string
}

// CheckDomain runs every check through one resolver and returns nil when all pass, otherwise the first
// problem in words the hotel's IT can act on.
func CheckDomain(ctx context.Context, r DoHResolver, c DomainCheck) error {
	txts, err := r.Query(ctx, "_onegate-verify."+c.FQDN, rrTXT)
	if err != nil {
		return err
	}
	found := false
	for _, t := range txts {
		if txtValue(t) == c.VerifyToken {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("TXT _onegate-verify.%s does not contain the verification token yet", c.FQDN)
	}
	if err := checkCNAME(ctx, r, c.FQDN, c.CNAMETarget); err != nil {
		return err
	}
	if err := checkCNAME(ctx, r, "_acme-challenge."+c.FQDN, c.ChallengeAlias); err != nil {
		return err
	}
	// CAA: climb from the name to its registrable domain; the first name with any CAA record decides
	// (RFC 8659 §3). The registrable domain is the top: above it lies a public suffix.
	reg, err := RegistrableDomain(c.FQDN)
	if err != nil {
		return err
	}
	for name := c.FQDN; ; {
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
			if ok, why := CAAPermits(set, c.AccountURL); !ok {
				return fmt.Errorf("CAA at %s: %s", name, why)
			}
			break
		}
		if name == reg {
			break
		}
		_, parent, _ := strings.Cut(name, ".")
		name = parent
	}
	return nil
}

func checkCNAME(ctx context.Context, r DoHResolver, name, want string) error {
	got, err := r.Query(ctx, name, rrCNAME)
	if err != nil {
		return err
	}
	for _, g := range got {
		if NormalizeFQDN(g) == NormalizeFQDN(want) {
			return nil
		}
	}
	if len(got) == 0 {
		return fmt.Errorf("CNAME %s is not published yet (it must point to %s.)", name, NormalizeFQDN(want))
	}
	return fmt.Errorf("CNAME %s points to %s, not %s.", name, NormalizeFQDN(got[0]), NormalizeFQDN(want))
}

// CheckDomainEverywhere runs CheckDomain through every resolver; all must pass.
func CheckDomainEverywhere(ctx context.Context, urls []string, hc *http.Client, c DomainCheck) error {
	type res struct {
		url string
		err error
	}
	ch := make(chan res, len(urls))
	for _, u := range urls {
		go func(u string) { ch <- res{u, CheckDomain(ctx, DoHResolver{URL: u, HTTP: hc}, c)} }(u)
	}
	var first error
	for range urls {
		if r := <-ch; r.err != nil && first == nil {
			first = r.err
		}
	}
	return first
}
