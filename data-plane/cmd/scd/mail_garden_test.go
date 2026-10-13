package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// THE SITE-WIDE MAIL ALLOWLIST IS NOT APPLIED WHILE TEMPORARY INTERNET IS ON (contract §6.6, §10.1): a mail
// service's addresses also serve the provider's other services, to every device that has not signed in, and the
// per-device temporary window already gives the device that asked for a code what it needs.
func TestTheMailAllowlistIsOffWhileTemporaryInternetIsOn(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?sslmode=disable") // never dialled
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	off := false
	for _, tc := range []struct {
		name string
		ta   *tenantcfg.TemporaryAccess
		want bool
	}{
		{"temporary internet on by default", nil, false},
		{"temporary internet switched off", &tenantcfg.TemporaryAccess{Enabled: &off}, true},
	} {
		ta := tc.ta
		s := &server{db: pool, methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{
				Email:  &tenantcfg.AuthMethod{Enabled: true, TemporaryAccess: ta},
				Portal: &tenantcfg.PortalConfig{MailProviders: []string{"gmail"}},
			}, nil
		}}
		got := len(s.mailGardenDomains(context.Background())) > 0
		if got != tc.want {
			t.Errorf("%s: mail allowlist applied=%v, want %v", tc.name, got, tc.want)
		}
	}
}
