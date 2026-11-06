package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestAResetCodeIsEightRandomDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newResetCode()
		if err != nil {
			t.Fatal(err)
		}
		if !resetCodePattern.MatchString(c) {
			t.Fatalf("code %q is not 8 digits", c)
		}
		seen[c] = true
	}
	if len(seen) < 195 {
		t.Fatalf("only %d different codes in 200", len(seen))
	}
}

func TestAResetCodeIsStoredOnlyAsAHashTiedToItsOperator(t *testing.T) {
	a := resetCodeHash("op-1", "12345678")
	if a == resetCodeHash("op-2", "12345678") {
		t.Fatal("the same code for two operators must not hash the same")
	}
	if a == resetCodeHash("op-1", "12345679") {
		t.Fatal("two codes must not hash the same")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) || regexp.MustCompile(`12345678`).MatchString(a) {
		t.Fatalf("hash %q", a)
	}
}

func TestTheResetBoundsMatchTheDatabaseChecks(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0214_an_operator_can_reset_a_forgotten_password_by_email.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range resetBounds {
		re := regexp.MustCompile(`(?m)^\s*` + b.Key + `\s+integer NOT NULL DEFAULT (\d+) CONSTRAINT \w+ CHECK \(` + b.Key + ` BETWEEN (\d+) AND (\d+)\)`)
		m := re.FindStringSubmatch(string(raw))
		if m == nil {
			t.Fatalf("no CHECK found for %s", b.Key)
		}
		def, _ := strconv.Atoi(m[1])
		lo, _ := strconv.Atoi(m[2])
		hi, _ := strconv.Atoi(m[3])
		if def != b.Default || lo != b.Min || hi != b.Max {
			t.Fatalf("%s: the console says %d (%d..%d), the database %d (%d..%d)", b.Key, b.Default, b.Min, b.Max, def, lo, hi)
		}
	}
	if !regexp.MustCompile(`enabled\s+boolean NOT NULL DEFAULT false`).Match(raw) ||
		!regexp.MustCompile(`allow_last_site_admin boolean NOT NULL DEFAULT false`).Match(raw) {
		t.Fatal("forgot password and the last site admin must both be off by default")
	}
}

func TestResetSettingsOutsideTheirBoundsAreRefused(t *testing.T) {
	ok := resetPolicy{CodeMinutes: 15, MaxAttempts: 5, AccountPerHour: 5, AddressPerHour: 20}
	if _, valid := validateResetPolicy(ok); !valid {
		t.Fatal("the defaults must be valid")
	}
	for _, bad := range []resetPolicy{
		{CodeMinutes: 4, MaxAttempts: 5, AccountPerHour: 5, AddressPerHour: 20},
		{CodeMinutes: 61, MaxAttempts: 5, AccountPerHour: 5, AddressPerHour: 20},
		{CodeMinutes: 15, MaxAttempts: 2, AccountPerHour: 5, AddressPerHour: 20},
		{CodeMinutes: 15, MaxAttempts: 5, AccountPerHour: 21, AddressPerHour: 20},
		{CodeMinutes: 15, MaxAttempts: 5, AccountPerHour: 5, AddressPerHour: 4},
	} {
		if msg, valid := validateResetPolicy(bad); valid || msg == "" {
			t.Fatalf("%+v was accepted", bad)
		}
	}
}

func TestOnlyAnActiveAccountThatIsNotTheLastSiteAdminCanReset(t *testing.T) {
	p := resetPolicy{}
	cases := []struct {
		a     resetAccount
		known bool
		allow bool
		want  string
	}{
		{resetAccount{}, false, false, "unknown_account"},
		{resetAccount{Active: false}, true, false, "account_not_active"},
		{resetAccount{Active: true, LastAdmin: true}, true, false, "last_site_admin"},
		{resetAccount{Active: true, LastAdmin: true}, true, true, ""},
		{resetAccount{Active: true}, true, false, ""},
	}
	for _, c := range cases {
		p.AllowLastSiteAdmin = c.allow
		if got := resetIneligible(c.a, c.known, p); got != c.want {
			t.Fatalf("%+v known=%v allow=%v: got %q, want %q", c.a, c.known, c.allow, got, c.want)
		}
	}
}

func TestTheOverviewSaysWhenForgotPasswordHasNoMailServer(t *testing.T) {
	items := deriveAttention(attentionInput{PasswordResetNoMail: true})
	found := false
	for _, it := range items {
		if it.ID == "password-reset-no-mail" {
			found = it.Severity == "warn" && it.Href == "/operators"
		}
	}
	if !found {
		t.Fatalf("no warning for forgot password without SMTP: %+v", items)
	}
	for _, it := range deriveAttention(attentionInput{}) {
		if it.ID == "password-reset-no-mail" {
			t.Fatal("the warning must not show when forgot password does not need mail")
		}
	}
}
