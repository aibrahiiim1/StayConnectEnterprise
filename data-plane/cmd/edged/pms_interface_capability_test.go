package main

// The authoring allowlist and validator offer only what pmsd can run, so an unsupported configuration cannot
// be authored (hiding it in the UI alone would leave it reachable from a script).

import (
	"strings"
	"testing"
)

// The authoring allowlist is exactly the set pmsd runs; both derive from the provider registry.
func TestPMSAllowedKinds_OnlyTheConnectorPMSDCanRun(t *testing.T) {
	want := map[string]bool{"protel-fias": true, "mews": true, "apaleo": true, "opera-cloud": true}
	if len(pmsAllowedKinds) != len(want) {
		t.Fatalf("the canonical set must be exactly the registry's runnable kinds %v, got %v", want, pmsAllowedKinds)
	}
	for k := range want {
		if !pmsAllowedKinds[k] {
			t.Fatalf("%q missing from the authoring allowlist", k)
		}
	}
	// Legacy scd kinds with no pmsd adapter, named individually so re-adding any one fails loudly.
	for _, unsupported := range []string{"opera-fias", "fidelio-fias", "stub"} {
		if pmsAllowedKinds[unsupported] {
			t.Fatalf("%q is offered as a canonical PMS Interface but pmsd does not support it: the revision "+
				"would be authored and published, then refused by the connector", unsupported)
		}
	}
}

// A new revision is authored unset; RESERVATION is recorded only by financial onboarding, and a legacy folio strategy is invalid.
func TestValidateRevisionConfig_ForcesUnsetFolioIdentity(t *testing.T) {
	for _, strategy := range []string{"RESERVATION", "GLOBALLY_UNIQUE", "UNIQUE_PER_STAY", "REUSED_SEQUENTIAL"} {
		in := validRevisionReq()
		in.PostingTargetModel = strategy
		if _, err := validateRevisionConfig(in); err == nil {
			t.Fatalf("%s was accepted; a posting target is recorded by financial onboarding, not a form", strategy)
		} else if !strings.Contains(err.Error(), "UNSET") {
			t.Fatalf("the refusal must name the required value, got %v", err)
		}
	}
	// Omitted entirely must default rather than fail.
	in := validRevisionReq()
	in.PostingTargetModel = ""
	if _, err := validateRevisionConfig(in); err != nil {
		t.Fatalf("an omitted posting target must default to UNSET, got %v", err)
	}
	if in.PostingTargetModel != postingTargetUnset {
		t.Fatalf("expected UNSET to be stamped, got %q", in.PostingTargetModel)
	}
}

// Implementation-controlled values are stamped; caller-sent values are discarded.
func TestValidateRevisionConfig_StampsImplementationControlledValues(t *testing.T) {
	in := validRevisionReq()
	in.NormalizationVersion = 99 // a number no build implements
	resyncOff := false
	in.ResyncSupported = &resyncOff
	in.ReadOnly = nil // the form no longer sends it

	if _, err := validateRevisionConfig(in); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if in.NormalizationVersion != canonicalNormalizationVersion {
		t.Fatalf("normalization version must come from the build, got %d", in.NormalizationVersion)
	}
	if in.ResyncSupported == nil || !*in.ResyncSupported {
		t.Fatal("resync support is a property of the adapter and must not be turned off by a caller")
	}
	if in.ReadOnly == nil || !*in.ReadOnly {
		t.Fatal("read-only must default to true rather than requiring the caller to assert it")
	}
}

// The supported link is unauthenticated, so NONE is the only valid credential mode (AUTH_KEY would never connect).
func TestValidateRevisionConfig_CredentialModeFollowsTheConnector(t *testing.T) {
	in := validRevisionReq()
	in.CredentialMode = "AUTH_KEY"
	if _, err := validateRevisionConfig(in); err == nil {
		t.Fatal("AUTH_KEY was accepted for a connector whose link carries no authentication")
	}

	in = validRevisionReq()
	in.CredentialMode = ""
	if _, err := validateRevisionConfig(in); err != nil {
		t.Fatalf("an omitted credential mode must default to NONE, got %v", err)
	}
	if in.CredentialMode != credentialModeNone {
		t.Fatalf("expected NONE to be stamped, got %q", in.CredentialMode)
	}
}

// Read-only stays fixed: an explicit false is refused.
func TestValidateRevisionConfig_ReadOnlyCannotBeTurnedOff(t *testing.T) {
	in := validRevisionReq()
	writable := false
	in.ReadOnly = &writable
	if _, err := validateRevisionConfig(in); err == nil {
		t.Fatal("a write-capable revision was accepted; pmsd refuses these and so must this path")
	}
}

func TestValidateRevisionConfig_KeepsOperatorFieldsConfigurable(t *testing.T) {
	in := validRevisionReq()
	in.Endpoint = "pms.example.local:5010"
	in.SourceTimezone = "Europe/Berlin"
	cfg, err := validateRevisionConfig(in)
	if err != nil {
		t.Fatalf("a valid operator configuration was refused: %v", err)
	}
	if cfg["endpoint"] != "pms.example.local:5010" {
		t.Fatalf("endpoint must reach the config, got %v", cfg["endpoint"])
	}

	bad := validRevisionReq()
	bad.SourceTimezone = "Mars/Olympus"
	if _, err := validateRevisionConfig(bad); err == nil {
		t.Fatal("an unloadable time zone must be refused: every arrival and departure is read in it")
	}
}

// validRevisionReq is the shape the form submits: endpoint, timezone, timeouts and currency only.
func validRevisionReq() *authorRevisionReq {
	return &authorRevisionReq{
		Endpoint:            "150.0.0.18:5003",
		SourceTimezone:      "Africa/Cairo",
		DialTimeoutMS:       10000,
		ReadTimeoutMS:       330000,
		WriteTimeoutMS:      10000,
		HeartbeatIntervalMS: 60000,
		HeartbeatTimeoutMS:  300000,
		FeedFreshnessMS:     900000,
		CompleteSyncMS:      86400000,
	}
}
