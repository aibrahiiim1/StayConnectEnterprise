package main

// THE CANONICAL PMS INTERFACE ONLY OFFERS WHAT pmsd CAN ACTUALLY RUN.
//
// These are unit tests on the authoring contract rather than HTTP tests, because the properties being
// asserted live in the allowlist and the validator — the two things a request has to get past — and proving
// them there covers every caller, not just the one a handler test would exercise.
//
// pmsd already refuses an unsupported kind: it declares supportedConnectorKinds and Revision.Validate
// returns REVISION_INVALID for anything else. What these assert is that the unsupported configuration
// cannot be AUTHORED — so an operator never creates, configures and publishes an Interface only to have the
// connector reject it at the last step. Hiding the option in the dropdown alone would leave it reachable
// from a script, a restored fixture, or the next screen somebody writes.

import (
	"strings"
	"testing"
)

func TestPMSAllowedKinds_OnlyTheConnectorPMSDCanRun(t *testing.T) {
	if len(pmsAllowedKinds) != 1 || !pmsAllowedKinds["protel-fias"] {
		t.Fatalf("the canonical set must be exactly {protel-fias} until a verified pmsd adapter exists for "+
			"another connector, got %v", pmsAllowedKinds)
	}
	// Named individually so that re-adding any one of them fails loudly rather than slipping in with a map
	// literal edit. These are the legacy scd kinds; their implementations still exist and are untouched.
	for _, unsupported := range []string{"opera-fias", "fidelio-fias", "mews", "apaleo", "stub"} {
		if pmsAllowedKinds[unsupported] {
			t.Fatalf("%q is offered as a canonical PMS Interface but pmsd does not support it: the revision "+
				"would be authored and published, then refused by the connector", unsupported)
		}
	}
}

// A new revision is authored UNSET and cannot be talked into a financial folio strategy from a form.
func TestValidateRevisionConfig_ForcesUnsetFolioIdentity(t *testing.T) {
	for _, strategy := range []string{"GLOBALLY_UNIQUE", "UNIQUE_PER_STAY", "REUSED_SEQUENTIAL"} {
		in := validRevisionReq()
		in.FolioIdentityStrategy = strategy
		if _, err := validateRevisionConfig(in); err == nil {
			t.Fatalf("%s was accepted; a folio strategy is a financial determination, not a form choice", strategy)
		} else if !strings.Contains(err.Error(), "UNSET") {
			t.Fatalf("the refusal must name the required value, got %v", err)
		}
	}
	// Omitted entirely is the normal case from the form, and must default rather than fail.
	in := validRevisionReq()
	in.FolioIdentityStrategy = ""
	if _, err := validateRevisionConfig(in); err != nil {
		t.Fatalf("an omitted folio strategy must default to UNSET, got %v", err)
	}
	if in.FolioIdentityStrategy != folioStrategyUnset {
		t.Fatalf("expected UNSET to be stamped, got %q", in.FolioIdentityStrategy)
	}
}

// Values the implementation controls are stamped, and anything the caller sent for them is discarded.
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

// The supported link is unauthenticated, so NONE is the only truthful credential mode. An interface saved as
// AUTH_KEY waits for a secret that will never exist and never connects.
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

// Read-only stays fixed: an explicit false is still refused, so the default is a convenience and not a hole.
func TestValidateRevisionConfig_ReadOnlyCannotBeTurnedOff(t *testing.T) {
	in := validRevisionReq()
	writable := false
	in.ReadOnly = &writable
	if _, err := validateRevisionConfig(in); err == nil {
		t.Fatal("a write-capable revision was accepted; pmsd refuses these and so must this path")
	}
}

// The genuinely site-specific fields are still the operator's, and are still validated.
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

// validRevisionReq is the shape the form now submits: endpoint, timezone, timeouts and currency only.
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
