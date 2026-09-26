package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

func TestMethodSwitchOnMatchesThePortal(t *testing.T) {
	on := &tenantcfg.AuthMethod{Enabled: true}
	off := &tenantcfg.AuthMethod{Enabled: false}
	cases := []struct {
		name   string
		cfg    *tenantcfg.AuthMethods
		method string
		want   bool
	}{
		{"no record", nil, guestMethodVoucher, false},
		{"voucher on", &tenantcfg.AuthMethods{Voucher: on}, guestMethodVoucher, true},
		{"voucher off", &tenantcfg.AuthMethods{Voucher: off}, guestMethodVoucher, false},
		{"voucher absent", &tenantcfg.AuthMethods{}, guestMethodVoucher, false},
		{"account on", &tenantcfg.AuthMethods{GuestAccount: on}, guestMethodAccount, true},
		{"account off while voucher on", &tenantcfg.AuthMethods{Voucher: on, GuestAccount: off}, guestMethodAccount, false},
		{"pms on", &tenantcfg.AuthMethods{PMS: &tenantcfg.PMSConfig{Enabled: true}}, guestMethodPMS, true},
		{"pms off", &tenantcfg.AuthMethods{PMS: &tenantcfg.PMSConfig{Enabled: false}}, guestMethodPMS, false},
		{"pms removed by licence", &tenantcfg.AuthMethods{Voucher: on}, guestMethodPMS, false},
		{"unknown method", &tenantcfg.AuthMethods{Voucher: on}, "email", false},
	}
	for _, c := range cases {
		if got := methodSwitchOn(c.cfg, c.method); got != c.want {
			t.Errorf("%s: methodSwitchOn = %v, want %v", c.name, got, c.want)
		}
	}
}
