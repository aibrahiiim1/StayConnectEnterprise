package portaltls

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// acmeClient returns a client registered (or found) at directoryURL. An account exists only at the CA that
// created it, so switching directory registers again. The account URL is cached for the current directory
// only; a client for another directory (an old issuer, for ARI or revocation) finds its account by key.
func (s *Service) acmeClient(ctx context.Context, st *Settings, directoryURL string) (*acme.Client, error) {
	key, err := s.Secrets.AccountKey()
	if err != nil {
		return nil, fmt.Errorf("ACME account key: %w", err)
	}
	hc, err := s.Opts.acmeHTTPClient()
	if err != nil {
		return nil, err
	}
	c := &acme.Client{Key: key, DirectoryURL: directoryURL, HTTPClient: hc, UserAgent: "onegate-central"}
	if directoryURL != s.Opts.directoryURL(st.ACMEDirectory) {
		return c, nil
	}
	if st.AccountURL != "" && st.AccountDirectory == directoryURL {
		c.KID = acme.KeyID(st.AccountURL)
		return c, nil
	}
	acct, err := c.Register(ctx, &acme.Account{Contact: []string{"mailto:" + st.ContactEmail}}, acme.AcceptTOS)
	if errors.Is(err, acme.ErrAccountAlreadyExists) {
		acct, err = c.GetReg(ctx, "")
	}
	if err != nil {
		return nil, fmt.Errorf("ACME account registration: %w", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE portal_tls_settings SET account_url=$1, account_directory=$2 WHERE id`, acct.URI, directoryURL); err != nil {
		return nil, err
	}
	st.AccountURL, st.AccountDirectory = acct.URI, directoryURL
	c.KID = acme.KeyID(acct.URI)
	AppendEvent(ctx, s.DB, "", "", EvAccountRegistered, ActorSystem, map[string]any{"account_url": acct.URI, "directory": directoryURL})
	return c, nil
}

// forgetAccountIfGone clears the cached account when the CA no longer knows it; the next call registers afresh.
func (s *Service) forgetAccountIfGone(ctx context.Context, err error) {
	var ae *acme.Error
	if errors.As(err, &ae) && strings.HasSuffix(ae.ProblemType, ":accountDoesNotExist") {
		_, _ = s.DB.Exec(ctx, `UPDATE portal_tls_settings SET account_url=NULL, account_directory=NULL WHERE id`)
	}
}

type issued struct {
	ChainPEM   string
	Serial     string
	NotBefore  time.Time
	NotAfter   time.Time
	RenewAfter time.Time
	OrderURL   string
}

// issue runs one ACME order, answering DNS-01 at challengeName. The TXT records are always deleted before it returns.
func (s *Service) issue(ctx context.Context, c *acme.Client, prov DNSProvider, hostname, challengeName string, csrDER []byte,
	setOrderURL func(string)) (*issued, error) {
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(hostname))
	if err != nil {
		return nil, fmt.Errorf("new order: %w", err)
	}
	// Only the newOrder answer's Location header carries the order URL.
	orderURL := order.URI
	setOrderURL(orderURL)

	var txtIDs []string
	defer func() {
		// The caller's context may be cancelled; clean up anyway.
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range txtIDs {
			_ = prov.DeleteRecord(cctx, id)
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, err := c.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range authz.Challenges {
			if ch.Type == "dns-01" {
				chal = ch
			}
		}
		if chal == nil {
			return nil, errors.New("the CA offered no dns-01 challenge")
		}
		value, err := c.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return nil, err
		}
		id, err := prov.SetTXT(ctx, challengeName, value)
		if err != nil {
			return nil, fmt.Errorf("publish challenge: %w", err)
		}
		txtIDs = append(txtIDs, id)
		servers := s.Opts.DNSCheckServers
		if len(servers) == 0 {
			if servers, err = prov.ListAuthoritativeNS(ctx); err != nil {
				return nil, fmt.Errorf("nameservers: %w", err)
			}
		}
		// Accept only once every authoritative server answers: a premature Accept is a failed validation that
		// counts against the CA's limit.
		if err := waitForTXT(ctx, servers, challengeName, value, s.Opts.propagationLimit(), s.Opts.propagationPoll()); err != nil {
			return nil, err
		}
		if _, err := c.Accept(ctx, chal); err != nil {
			return nil, fmt.Errorf("accept challenge: %w", err)
		}
		wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		_, err = c.WaitAuthorization(wctx, authzURL)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("validation: %w", err)
		}
	}
	wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	order, err = c.WaitOrder(wctx, orderURL)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("order not ready: %w", err)
	}
	der, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		// CreateOrderCert needs a Location header on the finalize answer, which RFC 8555 does not require
		// (Pebble omits it); wait on the known order URL instead. A failed finalize shows as an invalid order.
		wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		done, werr := c.WaitOrder(wctx, orderURL)
		cancel()
		if werr != nil || done.Status != acme.StatusValid || done.CertURL == "" {
			return nil, fmt.Errorf("finalize: %w", err)
		}
		if der, err = c.FetchCert(ctx, done.CertURL, true); err != nil {
			return nil, fmt.Errorf("fetch certificate: %w", err)
		}
	}
	out, err := checkIssuedChain(der, hostname, csrDER)
	if err != nil {
		return nil, err
	}
	out.OrderURL = orderURL
	return out, nil
}

// checkIssuedChain refuses anything but a leaf for exactly hostname carrying the CSR's key; the appliance checks again.
func checkIssuedChain(der [][]byte, hostname string, csrDER []byte) (*issued, error) {
	if len(der) == 0 {
		return nil, errors.New("the CA returned no certificate")
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, fmt.Errorf("issued certificate does not parse: %w", err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	type pubEq interface{ Equal(crypto.PublicKey) bool }
	if pk, ok := leaf.PublicKey.(pubEq); !ok || !pk.Equal(csr.PublicKey) {
		return nil, errors.New("the issued certificate does not carry the CSR's key")
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != hostname {
		return nil, fmt.Errorf("the issued certificate names %v, not %s", leaf.DNSNames, hostname)
	}
	var buf bytes.Buffer
	for _, d := range der {
		if _, err := x509.ParseCertificate(d); err != nil {
			return nil, fmt.Errorf("chain certificate does not parse: %w", err)
		}
		_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: d})
	}
	return &issued{
		ChainPEM:  buf.String(),
		Serial:    fmt.Sprintf("%x", leaf.SerialNumber),
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
	}, nil
}

// waitForTXT polls every server directly (no recursive resolver, no cache) until each answers with value.
func waitForTXT(ctx context.Context, servers []string, name, value string, limit, poll time.Duration) error {
	deadline := time.Now().Add(limit)
	pending := map[string]bool{}
	for _, s := range servers {
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(strings.TrimSuffix(s, "."), "53")
		}
		pending[s] = true
	}
	fq := strings.TrimSuffix(name, ".") + "."
	for {
		for srv := range pending {
			if txtVisibleAt(ctx, srv, fq, value) {
				delete(pending, srv)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			var left []string
			for srv := range pending {
				left = append(left, srv)
			}
			return fmt.Errorf("the challenge record %s is not visible at %s after %s", name, strings.Join(left, ", "), limit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func txtVisibleAt(ctx context.Context, server, fqdn, value string) bool {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	txts, err := r.LookupTXT(qctx, fqdn)
	if err != nil {
		return false
	}
	for _, t := range txts {
		if t == value {
			return true
		}
	}
	return false
}

// revokeCert treats a certificate the CA says is already revoked as revoked.
func revokeCert(ctx context.Context, c *acme.Client, leafDER []byte, reason string) error {
	code := acme.CRLReasonCessationOfOperation
	if reason == RevokeSuperseded {
		code = acme.CRLReasonSuperseded
	}
	err := c.RevokeCert(ctx, nil, leafDER, code)
	var ae *acme.Error
	if errors.As(err, &ae) && strings.HasSuffix(ae.ProblemType, ":alreadyRevoked") {
		return nil
	}
	return err
}

func leafFromChain(chainPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(chainPEM))
	if block == nil {
		return nil, errors.New("no certificate in the stored chain")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (s *Service) acmeHTTP() *http.Client {
	hc, err := s.Opts.acmeHTTPClient()
	if err != nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return hc
}
