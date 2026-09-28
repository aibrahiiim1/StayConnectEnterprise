package main

import (
	"context"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

func allMethodsOn() *tenantcfg.AuthMethods {
	on := func() *tenantcfg.AuthMethod { return &tenantcfg.AuthMethod{Enabled: true} }
	return &tenantcfg.AuthMethods{Email: on(), SMS: on(), WhatsApp: on(),
		Social: map[string]*tenantcfg.AuthMethod{"google": on(), "apple": on()}}
}

// A switched-on code or social method is offered only when something can deliver it.
func TestProviderReadinessHidesWhatNothingCanDeliver(t *testing.T) {
	s := &server{providerReadiness: func(context.Context) (map[string]bool, map[string]bool, bool) {
		return map[string]bool{"sms": true}, map[string]bool{"apple": true}, true
	}}
	cfg := allMethodsOn()
	s.applyProviderReadiness(context.Background(), cfg)
	if cfg.Email != nil || cfg.WhatsApp != nil || cfg.SMS == nil {
		t.Fatalf("channels: email=%v sms=%v whatsapp=%v", cfg.Email, cfg.SMS, cfg.WhatsApp)
	}
	if len(cfg.Social) != 1 || cfg.Social["apple"] == nil {
		t.Fatalf("social: %+v", cfg.Social)
	}
}

func TestProviderReadinessUnreadableFailsClosed(t *testing.T) {
	s := &server{providerReadiness: func(context.Context) (map[string]bool, map[string]bool, bool) { return nil, nil, false }}
	cfg := allMethodsOn()
	s.applyProviderReadiness(context.Background(), cfg)
	if cfg.Email != nil || cfg.SMS != nil || cfg.WhatsApp != nil || cfg.Social != nil {
		t.Fatalf("unreadable readiness kept a method: %+v", cfg)
	}
}
