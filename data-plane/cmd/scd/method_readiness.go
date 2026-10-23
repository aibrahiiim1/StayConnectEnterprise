package main

import (
	"context"
	"log/slog"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// Provider readiness is the fourth gate for a code or social sign-in: a licensed, deployed, enabled method is not offered
// until an enabled sender (email, sms, whatsapp) or social application can deliver it. "Test only" senders count: operators
// choose them on purpose to verify the flow.

// providerReadiness returns the channels with an enabled sender and the social providers with an enabled
// application. ok=false when it could not be read.
type providerReadinessFunc func(ctx context.Context) (channels, social map[string]bool, ok bool)

func (s *server) readProviderReadiness(ctx context.Context) (map[string]bool, map[string]bool, bool) {
	if s.providerReadiness != nil {
		return s.providerReadiness(ctx)
	}
	if s.db == nil {
		return nil, nil, false
	}
	channels, social := map[string]bool{}, map[string]bool{}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT channel FROM notification_providers WHERE tenant_id = $1 AND enabled`, s.tenID)
	if err != nil {
		slog.Warn("provider readiness: senders unreadable", "err", err)
		return nil, nil, false
	}
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			channels[c] = true
		}
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT DISTINCT provider FROM social_oauth_providers WHERE tenant_id = $1 AND enabled`, s.tenID)
	if err != nil {
		slog.Warn("provider readiness: social applications unreadable", "err", err)
		return nil, nil, false
	}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			social[p] = true
		}
	}
	rows.Close()
	return channels, social, true
}

// applyProviderReadiness removes code and social methods nothing can deliver. Unreadable readiness removes
// them all (fail closed): the portal then offers only methods that need no provider.
func (s *server) applyProviderReadiness(ctx context.Context, cfg *tenantcfg.AuthMethods) {
	if cfg == nil || (s.providerReadiness == nil && s.db == nil) {
		return
	}
	channels, social, ok := s.readProviderReadiness(ctx)
	// Email accounts are offered only with an email sender: with none, no verification or reset link could ever be delivered
	// and every account would be suspended at its deadline. The register route keeps working for an already-open page (the switch, not readiness, gates it).
	if !ok || !channels["email"] {
		cfg.EmailAccount = nil
	}
	if !ok || !channels["sms"] {
		cfg.SMS = nil
	}
	if !ok || !channels["whatsapp"] {
		cfg.WhatsApp = nil
	}
	for p := range cfg.Social {
		if !ok || !social[p] {
			delete(cfg.Social, p)
		}
	}
	if len(cfg.Social) == 0 {
		cfg.Social = nil
	}
}
