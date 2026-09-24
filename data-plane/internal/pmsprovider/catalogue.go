package pmsprovider

// The catalogue entries. Every endpoint, header and field named in help text below was taken from the
// provider's published documentation (see each DocsURL); none of the REST connectors has been exercised
// against the real service, which is exactly what their verification status says.

const restVerificationNote = "Built from the provider's published API documentation and proven by automated " +
	"contract tests that replay documentation-shaped responses (paging, rate limiting, authentication " +
	"failures, check-outs and room moves). It has not yet been connected to a live property: verify it " +
	"against your own property before relying on it for guest sign-in."

// sourceTimezoneField is common to every connector: the property's IANA time zone, used to turn provider
// timestamps into the property's calendar dates exactly as the FIAS feed reports them.
func sourceTimezoneField() Field {
	return Field{
		Key: "source_timezone", Label: "Property time zone", Type: FieldString, Required: true,
		Default: "Africa/Cairo", Placeholder: "Europe/Berlin",
		Help: "The IANA time zone the hotel operates in. Arrival and departure dates are read in it.",
	}
}

func maxAuthCacheField() Field {
	return Field{
		Key: "max_auth_cache_age_seconds", Label: "Offline sign-in window", Type: FieldInt,
		Min: i64(0), Max: i64(604800), Unit: "seconds",
		Help: "How long the last good guest list may still authorise room sign-in while the PMS cannot be " +
			"reached. Leave empty for the default (one full refresh interval plus the keep-alive allowance).",
	}
}

func protelFIAS() Provider {
	ms := func(key, label string, def int64, help string) Field {
		return Field{Key: key, Label: label, Type: FieldInt, Required: true, Default: def,
			Min: i64(1), Max: i64(86_400_000), Unit: "milliseconds", Help: help}
	}
	return Provider{
		Kind: KindProtelFIAS, Label: "Protel (FIAS)", Vendor: "protel hotelsoftware",
		Integration: "FIAS interface over a TCP socket (read-only)",
		Transport:   TransportSocket, Verification: VerifiedLive,
		VerificationNote: "Connected and accepted against a live Protel installation: guest in, guest change, " +
			"guest out and full guest-list resynchronisation are all proven on a real property.",
		DocsURL:    "https://www.protel.net/",
		Credential: Credential{Mode: CredentialNone, Fields: []CredentialField{}},
		Fields: []Field{
			{Key: "endpoint", Label: "PMS address", Type: FieldHostPort, Required: true,
				Placeholder: "pms.example.local:5010",
				Help:        "The host and port of the Protel FIAS interface, as host:port."},
			sourceTimezoneField(),
			ms("dial_timeout_ms", "Connect timeout", 5000, "How long to wait for the TCP connection to open."),
			ms("read_timeout_ms", "Read timeout", 15000, "How long a single read may take."),
			ms("write_timeout_ms", "Write timeout", 15000, "How long a single write may take."),
			ms("heartbeat_interval_ms", "Keep-alive every", 30000, "How often the link is kept alive when idle."),
			ms("heartbeat_timeout_ms", "Keep-alive timeout", 90000, "Must be greater than the keep-alive interval."),
			ms("feed_freshness_ms", "Guest list considered stale after", 120000, "Freshness bound for the feed."),
			ms("complete_sync_ms", "Full guest list refresh at least every", 600000, "Bound on the age of the last complete guest list."),
			maxAuthCacheField(),
			{Key: "financial_base_currency", Label: "Base currency", Type: FieldString,
				Placeholder: "EUR", Help: "Optional three-letter ISO code. Set together with the exponent, or leave both empty."},
			{Key: "financial_base_currency_exponent", Label: "Currency exponent", Type: FieldInt,
				Min: i64(0), Max: i64(4), Help: "Minor-unit digits of the base currency (2 for EUR). Set together with the currency."},
		},
		Capabilities: Capabilities{FullResync: true, LiveEvents: true, TestConnection: false, Arrivals: true, Departures: true},
		SetupSteps: []string{
			"Ask the property's Protel administrator to enable a FIAS interface for this appliance and note its host and port.",
			"Enter the address and the property's time zone, save, then publish the configuration.",
			"Activate the connection. The link status shows when the first full guest list has arrived.",
			"Protel accepts one link at a time: do not point a second system at the same FIAS port.",
		},
	}
}

func restTimingFields(pollDefault int64) []Field {
	return []Field{
		sourceTimezoneField(),
		{Key: "poll_interval_seconds", Label: "Check for changes every", Type: FieldInt, Required: true,
			Default: pollDefault, Min: i64(15), Max: i64(900), Unit: "seconds",
			Help: "How often the PMS is asked what changed. Each successful check also counts as a keep-alive."},
		{Key: "request_timeout_seconds", Label: "Request timeout", Type: FieldInt, Required: true,
			Default: int64(30), Min: i64(5), Max: i64(120), Unit: "seconds",
			Help: "How long a single request to the PMS may take."},
		{Key: "full_resync_minutes", Label: "Full guest list refresh every", Type: FieldInt, Required: true,
			Default: int64(60), Min: i64(5), Max: i64(1440), Unit: "minutes",
			Help: "How often the complete in-house list is re-read, in addition to the check for changes."},
		maxAuthCacheField(),
	}
}

func mews() Provider {
	fields := []Field{
		{Key: "platform_url", Label: "Platform address", Type: FieldURL, Required: true,
			Default: "https://api.mews.com", Placeholder: "https://api.mews.com",
			Help: "https://api.mews.com for production, https://api.mews-demo.com for the Mews demo environment."},
		{Key: "enterprise_id", Label: "Enterprise ID", Type: FieldString,
			Pattern: `^[0-9a-fA-F-]{36}$`, Placeholder: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
			Help: "Only needed when the access token covers several properties (a portfolio token)."},
		{Key: "service_id", Label: "Accommodation service ID", Type: FieldString,
			Pattern: `^[0-9a-fA-F-]{36}$`,
			Help:    "Optional. Limits the guest list to one bookable service, normally the property's accommodation service."},
	}
	fields = append(fields, restTimingFields(60)...)
	return Provider{
		Kind: KindMews, Label: "Mews", Vendor: "Mews Systems",
		Integration: "Mews Connector API (REST, polling)",
		Transport:   TransportRESTPoll, Verification: VerifiedContract, VerificationNote: restVerificationNote,
		DocsURL: "https://docs.mews.com/connector-api",
		Credential: Credential{Mode: CredentialAuthKey, Fields: []CredentialField{
			{Key: "client_token", Label: "Client token", Secret: true, Required: true,
				Help: "Issued by Mews to the integration partner."},
			{Key: "access_token", Label: "Access token", Secret: true, Required: true,
				Help: "Issued for this property when the integration is connected in Mews Marketplace."},
		}},
		Fields:       fields,
		Capabilities: Capabilities{FullResync: true, LiveEvents: true, TestConnection: true, Arrivals: true, Departures: true},
		SetupSteps: []string{
			"Connect the integration to the property in Mews Marketplace and obtain the client token and access token.",
			"Create the connection, enter the platform address and the property's time zone, then save and publish.",
			"Store the client token and access token as the connection's credential.",
			"Use Test connection to confirm the tokens are accepted, then activate the connection.",
		},
	}
}

func apaleo() Provider {
	fields := []Field{
		{Key: "api_url", Label: "API address", Type: FieldURL, Required: true,
			Default: "https://api.apaleo.com", Help: "The Apaleo API address."},
		{Key: "identity_url", Label: "Token address", Type: FieldURL, Required: true,
			Default: "https://identity.apaleo.com/connect/token",
			Help:    "The Apaleo identity server's token endpoint used for the client-credentials sign-in."},
		{Key: "property_id", Label: "Property ID", Type: FieldString, Required: true,
			Pattern: `^[A-Za-z0-9_-]{1,32}$`, Placeholder: "MUC",
			Help: "The Apaleo property code, for example MUC."},
	}
	fields = append(fields, restTimingFields(60)...)
	return Provider{
		Kind: KindApaleo, Label: "Apaleo", Vendor: "Apaleo",
		Integration: "Apaleo Booking API (REST, OAuth 2.0 client credentials, polling)",
		Transport:   TransportRESTPoll, Verification: VerifiedContract, VerificationNote: restVerificationNote,
		DocsURL: "https://api.apaleo.com/swagger/index.html?urls.primaryName=Booking%20V1",
		Credential: Credential{Mode: CredentialAuthKey, Fields: []CredentialField{
			{Key: "client_id", Label: "Client ID", Secret: false, Required: true,
				Help: "From the custom (simple client) app registered in the property's Apaleo account."},
			{Key: "client_secret", Label: "Client secret", Secret: true, Required: true,
				Help: "Shown once when the app is registered."},
		}},
		Fields:       fields,
		Capabilities: Capabilities{FullResync: true, LiveEvents: true, TestConnection: true, Arrivals: true, Departures: true},
		SetupSteps: []string{
			"In the property's Apaleo account, register a custom app (client-credentials client) with read access to reservations and inventory.",
			"Create the connection, enter the property ID and the property's time zone, then save and publish.",
			"Store the client ID and client secret as the connection's credential.",
			"Use Test connection to confirm the sign-in and a reservation read, then activate the connection.",
		},
	}
}

func operaCloud() Provider {
	fields := []Field{
		{Key: "gateway_url", Label: "OHIP gateway address", Type: FieldURL, Required: true,
			Placeholder: "https://your-gateway.hospitality-api.example.com",
			Help:        "The Oracle Hospitality Integration Platform gateway URL for the property's region."},
		{Key: "hotel_id", Label: "Hotel code", Type: FieldString, Required: true,
			Pattern: `^[A-Za-z0-9_-]{1,20}$`, Placeholder: "HOTEL1",
			Help: "The OPERA Cloud hotel code (sent as x-hotelid)."},
		{Key: "scope", Label: "OAuth scope", Type: FieldString, Required: true,
			Help: "The scope assigned to your OHIP application for the client-credentials grant."},
		{Key: "enterprise_id", Label: "Enterprise ID", Type: FieldString,
			Pattern: `^[A-Z0-9]{1,8}$`,
			Help:    "Only when OHIP requires it for client-credentials sign-in (sent as the enterpriseId header)."},
	}
	fields = append(fields, restTimingFields(120)...)
	return Provider{
		Kind: KindOperaCloud, Label: "Oracle OPERA Cloud", Vendor: "Oracle Hospitality",
		Integration: "OPERA Cloud via Oracle Hospitality Integration Platform (REST, polling)",
		Transport:   TransportRESTPoll, Verification: VerifiedContract, VerificationNote: restVerificationNote,
		DocsURL: "https://docs.oracle.com/en/industries/hospitality/integration-platform/",
		Credential: Credential{Mode: CredentialAuthKey, Fields: []CredentialField{
			{Key: "client_id", Label: "Client ID", Secret: false, Required: true,
				Help: "The OHIP integration's client ID."},
			{Key: "client_secret", Label: "Client secret", Secret: true, Required: true,
				Help: "The OHIP integration's client secret."},
			{Key: "app_key", Label: "Application key", Secret: true, Required: true,
				Pattern: `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
				Help:    "The OHIP application key (sent as x-app-key)."},
		}},
		Fields: fields,
		// OPERA Cloud's reservation list offers no documented "changed since" filter, so changes are found by
		// re-reading the in-house list on every check and comparing it with the last one. Rooms cannot be
		// enumerated through the reservation API, so roster reconciliation stays with explicit check-outs.
		Capabilities: Capabilities{FullResync: true, LiveEvents: true, TestConnection: true, Arrivals: true, Departures: true},
		SetupSteps: []string{
			"Register an integration in the OHIP developer portal for the property's OPERA Cloud environment and note the gateway URL, client ID, client secret, application key and scope.",
			"Create the connection, enter the gateway, hotel code, scope and the property's time zone, then save and publish.",
			"Store the client ID, client secret and application key as the connection's credential.",
			"Use Test connection to confirm the sign-in and a reservation read, then activate the connection.",
		},
	}
}
