package portaltls

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxOrdersPerDay = 10
)

type OrderRequest struct {
	Hostname string `json:"hostname"`
	CSRPEM   string `json:"csr_pem"`
}

type OrderAccepted struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

// CreateOrder validates the request and queues it for the worker. It never talks to the CA itself: an
// appliance request must not hold a Central goroutine for minutes.
func (s *Service) CreateOrder(ctx context.Context, applianceID string, in OrderRequest) (*OrderAccepted, error) {
	if _, err := s.requireActive(ctx, applianceID); err != nil {
		return nil, err
	}
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	if ok, _ := s.Availability(st); !ok {
		return nil, refuse(http.StatusServiceUnavailable, "not_configured", "Central has no DNS/ACME configuration yet")
	}
	hostname := NormalizeFQDN(in.Hostname)
	h, err := scanHost(s.DB.QueryRow(ctx, `SELECT `+hostCols+` FROM portal_hostnames WHERE appliance_id=$1`, applianceID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	hostKnown := err == nil

	var csr *x509.CertificateRequest
	kind := ""
	if sp := s.Opts.Shared(); sp.Enabled() && hostname == sp.Hostname {
		// THE SHARED NAME: this appliance's own certificate for it, naming its OneGate hostname too.
		if !hostKnown || h.ReleasedAt != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "unknown_hostname", "this appliance has no live OneGate hostname yet")
		}
		if h.PortalIP != sp.Address.String() {
			return nil, refuse(http.StatusUnprocessableEntity, "shared_not_eligible",
				"the shared portal name answers %s; this appliance's portal address is %s", sp.Address, h.PortalIP)
		}
		if csr, err = ParseAndCheckSharedCSR(in.CSRPEM, sp.Hostname, h.FQDN); err != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "invalid_csr", "%s", err.Error())
		}
		// Re-checked before every order, as a hotel domain is.
		if ok, why := s.sharedReady(ctx, st, true); !ok {
			return nil, refuse(http.StatusUnprocessableEntity, "shared_not_ready", "the shared portal name is not ready in public DNS: %s", why)
		}
		kind = KindOneGate
	} else {
		var name string
		csr, name, err = ParseAndCheckCSR(in.CSRPEM)
		if err != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "invalid_csr", "%s", err.Error())
		}
		if name != hostname {
			return nil, refuse(http.StatusUnprocessableEntity, "invalid_csr", "the CSR names %s, not %s", name, hostname)
		}
	}

	switch {
	case kind != "":
		// The shared name, decided above.
	case hostKnown && h.FQDN == hostname:
		if h.ReleasedAt != nil {
			return nil, refuse(http.StatusUnprocessableEntity, "hostname_released", "this hostname was released")
		}
		kind = KindOneGate
	default:
		d, err := s.liveDomain(ctx, s.DB, applianceID)
		if err != nil {
			return nil, err
		}
		if d == nil || d.FQDN != hostname {
			return nil, refuse(http.StatusUnprocessableEntity, "unknown_hostname", "%s is neither this appliance's OneGate hostname nor its custom domain", hostname)
		}
		if d.Status != "VERIFIED" {
			return nil, refuse(http.StatusUnprocessableEntity, "domain_not_verified", "the custom domain %s is not verified", hostname)
		}
		// Re-verify before every order: a delegation that broke since the last check must not yield a certificate.
		if !s.checkAndRecordDomain(ctx, d, h.FQDN, st) {
			return nil, refuse(http.StatusUnprocessableEntity, "domain_not_verified", "the custom domain no longer verifies: %s", d.LastError)
		}
		kind = KindCustom
	}

	if id, ok := s.openOrder(ctx, applianceID, hostname); ok {
		return nil, &Refusal{Status: http.StatusConflict, Code: "order_open", Message: "an order is already open for this hostname",
			Extra: map[string]any{"order_id": id}}
	}
	// The daily cap is counted and the order inserted in one transaction under a per-appliance lock, so
	// concurrent requests cannot all pass the count (audit Phase 1 L18).
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('portal_tls_orders'), hashtext($1))`, applianceID); err != nil {
		return nil, err
	}
	var today int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM portal_certificate_orders WHERE appliance_id=$1 AND created_at > now() - interval '24 hours'`,
		applianceID).Scan(&today); err != nil {
		return nil, err
	}
	if today >= maxOrdersPerDay {
		return nil, refuse(http.StatusTooManyRequests, "rate_limited", "at most %d certificate orders per appliance per day", maxOrdersPerDay)
	}

	var id string
	err = tx.QueryRow(ctx, `
        INSERT INTO portal_certificate_orders (appliance_id, hostname, kind, csr_der)
        VALUES ($1,$2,$3,$4) RETURNING id::text`, applianceID, hostname, kind, csr.Raw).Scan(&id)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if isUniqueViolation(err) {
		if oid, ok := s.openOrder(ctx, applianceID, hostname); ok {
			return nil, &Refusal{Status: http.StatusConflict, Code: "order_open", Message: "an order is already open for this hostname",
				Extra: map[string]any{"order_id": oid}}
		}
	}
	if err != nil {
		return nil, err
	}
	AppendEvent(ctx, s.DB, applianceID, hostname, EvOrderQueued, ActorSystem, map[string]any{"order_id": id, "kind": kind})
	s.Nudge()
	return &OrderAccepted{OrderID: id, Status: "QUEUED"}, nil
}

func (s *Service) openOrder(ctx context.Context, applianceID, hostname string) (string, bool) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id::text FROM portal_certificate_orders WHERE appliance_id=$1 AND hostname=$2 AND status IN ('QUEUED','PROCESSING')`,
		applianceID, hostname).Scan(&id)
	return id, err == nil
}

type OrderView struct {
	OrderID    string     `json:"order_id"`
	Hostname   string     `json:"hostname"`
	Status     string     `json:"status"`
	ChainPEM   *string    `json:"chain_pem,omitempty"`
	Serial     *string    `json:"serial"`
	NotAfter   *time.Time `json:"not_after"`
	RenewAfter *time.Time `json:"renew_after"`
	Error      *string    `json:"error"`
}

// GetOrder returns only the appliance's own orders. The chain is returned only while ISSUED.
func (s *Service) GetOrder(ctx context.Context, applianceID, orderID string) (*OrderView, error) {
	var v OrderView
	var chain, serial, errText *string
	err := s.DB.QueryRow(ctx, `
        SELECT id::text, hostname, status, chain_pem, serial, not_after, renew_after, error
          FROM portal_certificate_orders WHERE id::text=$1 AND appliance_id=$2`, orderID, applianceID).
		Scan(&v.OrderID, &v.Hostname, &v.Status, &chain, &serial, &v.NotAfter, &v.RenewAfter, &errText)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, refuse(http.StatusNotFound, "not_found", "no such order")
	}
	if err != nil {
		return nil, err
	}
	if v.Status == "ISSUED" {
		v.ChainPEM = chain
	}
	v.Serial, v.Error = serial, errText
	return &v, nil
}
