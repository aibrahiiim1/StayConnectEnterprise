package iamv2

import "testing"

// env map -> Getenv
func envGetter(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestPMSConfig_DefaultAllOff(t *testing.T) {
	c := DefaultPMSConfig()
	if c.MasterEnabled || c.Enabled() || c.ConnectorOn() || c.IngestOn() || c.AuthOn() || c.CheckoutGraceOn() || c.AdminOn() {
		t.Fatalf("default config must be all-OFF, got %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate, got %v", err)
	}
}

func TestPMSConfig_LoadFromEnv_Table(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr bool
		checks  func(t *testing.T, c PMSConfig)
	}{
		{
			name: "empty env => all OFF, valid",
			env:  map[string]string{},
			checks: func(t *testing.T, c PMSConfig) {
				if c.Enabled() || c.MasterEnabled {
					t.Fatalf("want all-off, got %+v", c)
				}
			},
		},
		{
			name: "master only => no surface live (Enabled false), valid",
			env:  map[string]string{EnvPhase3Master: "true"},
			checks: func(t *testing.T, c PMSConfig) {
				if !c.MasterEnabled {
					t.Fatal("master must be on")
				}
				if c.Enabled() || c.ConnectorOn() || c.AuthOn() {
					t.Fatalf("no child on => no surface live, got %+v", c)
				}
			},
		},
		{
			name: "master+connector => ConnectorOn only",
			env:  map[string]string{EnvPhase3Master: "true", EnvPhase3PMSConnector: "true"},
			checks: func(t *testing.T, c PMSConfig) {
				if !c.ConnectorOn() || !c.Enabled() {
					t.Fatalf("connector must be live, got %+v", c)
				}
				if c.AuthOn() || c.IngestOn() || c.AdminOn() || c.CheckoutGraceOn() {
					t.Fatalf("only connector should be live, got %+v", c)
				}
			},
		},
		{
			name: "master+all children => all surfaces live",
			env: map[string]string{
				EnvPhase3Master: "1", EnvPhase3PMSConnector: "1", EnvPhase3PMSIngest: "1",
				EnvPhase3PMSAuth: "1", EnvPhase3CheckoutGrace: "1", EnvPhase3Admin: "1",
			},
			checks: func(t *testing.T, c PMSConfig) {
				if !(c.ConnectorOn() && c.IngestOn() && c.AuthOn() && c.CheckoutGraceOn() && c.AdminOn() && c.Enabled()) {
					t.Fatalf("all surfaces must be live, got %+v", c)
				}
			},
		},
		// incoherent: each child ON while master OFF must fail closed.
		{name: "connector ON, master OFF => reject", env: map[string]string{EnvPhase3PMSConnector: "true"}, wantErr: true},
		{name: "ingest ON, master OFF => reject", env: map[string]string{EnvPhase3PMSIngest: "true"}, wantErr: true},
		{name: "auth ON, master OFF => reject", env: map[string]string{EnvPhase3PMSAuth: "true"}, wantErr: true},
		{name: "grace ON, master OFF => reject", env: map[string]string{EnvPhase3CheckoutGrace: "true"}, wantErr: true},
		{name: "admin ON, master OFF => reject", env: map[string]string{EnvPhase3Admin: "true"}, wantErr: true},
		// malformed booleans fail closed.
		{name: "malformed master => reject", env: map[string]string{EnvPhase3Master: "yesish"}, wantErr: true},
		{name: "malformed child => reject", env: map[string]string{EnvPhase3Master: "true", EnvPhase3PMSAuth: "onnn"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadPMSConfigFromEnv(envGetter(tc.env))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got config %+v", c)
				}
				if c != (PMSConfig{}) {
					t.Fatalf("on error config must be zero-value (fail closed), got %+v", c)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.checks != nil {
				tc.checks(t, c)
			}
		})
	}
}

// Exhaustive: for every one of the 2^6 flag combinations, Validate/Enabled must be self-consistent and
// no surface may be ON while master is OFF.
func TestPMSConfig_Exhaustive(t *testing.T) {
	for bits := 0; bits < 64; bits++ {
		c := PMSConfig{
			MasterEnabled:        bits&1 != 0,
			PMSConnectorEnabled:  bits&2 != 0,
			PMSIngestEnabled:     bits&4 != 0,
			PMSAuthEnabled:       bits&8 != 0,
			CheckoutGraceEnabled: bits&16 != 0,
			AdminEnabled:         bits&32 != 0,
		}
		err := c.Validate()
		if !c.MasterEnabled && c.anyChildSet() {
			if err == nil {
				t.Fatalf("bits=%d: child ON while master OFF must fail Validate", bits)
			}
			continue
		}
		if err != nil {
			t.Fatalf("bits=%d: coherent config must validate, got %v", bits, err)
		}
		// No surface may be live unless master is on.
		if !c.MasterEnabled && (c.ConnectorOn() || c.IngestOn() || c.AuthOn() || c.CheckoutGraceOn() || c.AdminOn() || c.Enabled()) {
			t.Fatalf("bits=%d: a surface reported live while master OFF", bits)
		}
	}
}

func TestPMSConfig_SafeFlagSummary_NoSecrets(t *testing.T) {
	c := PMSConfig{MasterEnabled: true, PMSAuthEnabled: true}
	s := c.SafeFlagSummary()
	if s == "" || len(s) > 200 {
		t.Fatalf("unexpected summary: %q", s)
	}
	// Must contain only flag booleans, never secret-like content.
	for _, want := range []string{"phase3 master=true", "auth=true", "connector=false"} {
		if !contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
