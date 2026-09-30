package main

import (
	"context"
	"log/slog"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// PROVIDER READINESS, THE FOURTH GATE FOR A CODE OR SOCIAL SIGN-IN. A method that is licensed, deployed and
// switched on is still not offered to a client until something can deliver it: an enabled sender for its
// channel (email, sms, whatsapp), or an enabled application for a social provider. Without that the client
// would be asked for an address or sent to a provider and then fail. "Test only" senders count: an operator
// chose them on purpose, and they are how the flow is verified without a real provider.

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
	if !ok || !channels["email"] {
		cfg.Email = nil
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
