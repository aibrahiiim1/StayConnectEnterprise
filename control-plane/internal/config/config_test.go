package config

import "testing"

// X-8: hardened defaults, and a production ctrlapi refuses to start weakened.
func TestHardenedDefaults(t *testing.T) {
	t.Setenv("CTRLAPI_ADDR", "")
	t.Setenv("CTRLAPI_COOKIE_SECURE", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:8080" || !c.CookieSecure {
		t.Fatalf("defaults must be loopback and Secure cookies: %+v", c)
	}
	t.Setenv("CTRLAPI_COOKIE_SECURE", "false")
	if c, _ = Load(); c.CookieSecure {
		t.Fatal("only an explicit false turns Secure off")
	}
}

func TestProductionRefusesWeakenedListenerAndCookie(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080"} {
		errs, _ := Config{Env: "production", Addr: addr, CookieSecure: true}.Validate()
		if len(errs) != 1 {
			t.Errorf("%s in production must refuse to start, got %v", addr, errs)
		}
		if errs, _ := (Config{Env: "production", Addr: addr, CookieSecure: true, AllowPublicListen: true}).Validate(); len(errs) != 0 {
			t.Errorf("%s with the explicit opt-out is allowed, got %v", addr, errs)
		}
	}
	if errs, _ := (Config{Env: "production", Addr: "127.0.0.1:8080", CookieSecure: false}).Validate(); len(errs) != 1 {
		t.Fatalf("insecure cookies in production must refuse to start, got %v", errs)
	}
	if errs, _ := (Config{Env: "production", Addr: "127.0.0.1:8080", CookieSecure: true}).Validate(); len(errs) != 0 {
		t.Fatalf("the hardened configuration starts: %v", errs)
	}
	errs, warns := Config{Env: "dev", Addr: ":8080", CookieSecure: false}.Validate()
	if len(errs) != 0 || len(warns) != 2 {
		t.Fatalf("development only warns: errs=%v warns=%v", errs, warns)
	}
}
