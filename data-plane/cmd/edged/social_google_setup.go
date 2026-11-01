package main

// The Admin Console's Google setup checklist. Every Site owns its Google Cloud project, its OAuth client and its portal
// domain; this appliance authenticates its own Clients and nothing about it goes through OneGate Central. The checklist
// separates what this appliance can verify itself ("ok" / "todo") from what only Google can confirm ("manual": we say so
// instead of guessing), and every "todo" says what to do next.

import (
	"strings"
)

type socialSetupStep struct {
	ID    string `json:"id"`
	State string `json:"state"` // ok | todo | manual
	Title string `json:"title"`
	// Detail says what is wrong and what to do, or what the step means.
	Detail string `json:"detail"`
	// Href is the Admin Console page that fixes it, when there is one.
	Href string `json:"href,omitempty"`
}

const googleClientIDSuffix = ".apps.googleusercontent.com"

// googleSetupSteps is a pure function of the provider row and the portal state, so it is testable without a database.
func googleSetupSteps(p edgeSocialProvider, st portalTLSState) []socialSetupStep {
	domain := strings.ToLower(strings.TrimSpace(st.CustomDomain))
	redirect := ""
	if domain != "" {
		redirect = "https://" + domain + socialCallbackPath
	}
	var out []socialSetupStep

	// 1. The Site's own portal domain with a certificate.
	switch {
	case domain == "":
		out = append(out, socialSetupStep{ID: "site_domain", State: "todo", Href: "/portal-https",
			Title: "Give this Site its own portal domain",
			Detail: "Google only accepts a sign-in address on a domain you own and verify. Enter a domain of yours (for example " +
				"wifi.example.com) on Portal HTTPS, publish the DNS records it shows, and wait until it is verified."})
	case st.CustomDomainStatus != "VERIFIED":
		detail := "Publish the DNS records shown on Portal HTTPS for " + domain + " and press Check now."
		if e := strings.TrimSpace(st.CustomDomainError); e != "" {
			detail = "Not verified yet: " + e
		}
		out = append(out, socialSetupStep{ID: "site_domain", State: "todo", Href: "/portal-https",
			Title: "Your portal domain " + domain + " is not verified yet", Detail: detail})
	case !st.SiteDomainUsable:
		out = append(out, socialSetupStep{ID: "site_domain", State: "todo", Href: "/portal-https",
			Title:  "Waiting for the certificate of " + domain,
			Detail: "The domain is verified; its HTTPS certificate is being obtained and installed. This usually takes a few minutes."})
	default:
		out = append(out, socialSetupStep{ID: "site_domain", State: "ok",
			Title: "Your portal domain " + domain + " is verified and has a valid certificate", Detail: ""})
	}

	// 2. HTTPS on the portal at all.
	if st.HTTPSActive {
		out = append(out, socialSetupStep{ID: "https", State: "ok", Title: "The Client Portal is on HTTPS"})
	} else {
		out = append(out, socialSetupStep{ID: "https", State: "todo", Href: "/portal-https",
			Title:  "The Client Portal is not on HTTPS yet",
			Detail: "Social sign-in is offered only over HTTPS. See Portal HTTPS for what is missing."})
	}

	// 3. The OAuth client, as stored here.
	cid := strings.TrimSpace(p.ClientID)
	switch {
	case cid == "":
		out = append(out, socialSetupStep{ID: "client_id", State: "todo",
			Title:  "Enter the OAuth Client ID",
			Detail: "In Google Cloud Console open APIs & Services, then Credentials, create an OAuth client of type Web application, and paste its Client ID here."})
	case !strings.HasSuffix(strings.ToLower(cid), googleClientIDSuffix):
		out = append(out, socialSetupStep{ID: "client_id", State: "todo",
			Title:  "The Client ID does not look like a Google one",
			Detail: "A Google Client ID ends in " + googleClientIDSuffix + ". Copy it from the OAuth client in your Google Cloud project."})
	default:
		out = append(out, socialSetupStep{ID: "client_id", State: "ok", Title: "Client ID entered"})
	}
	if p.SecretSet {
		out = append(out, socialSetupStep{ID: "client_secret", State: "ok", Title: "Client secret stored on this appliance (write-only, encrypted)"})
	} else {
		out = append(out, socialSetupStep{ID: "client_secret", State: "todo",
			Title:  "Enter the OAuth Client secret",
			Detail: "Paste the client secret of the same OAuth client. It is stored encrypted on this appliance only, and can never be read back."})
	}

	// 4. The callback address registered for the Site's domain.
	if redirect != "" {
		registered := false
		for _, h := range p.CallbackHosts {
			if strings.EqualFold(strings.TrimSpace(h), domain) {
				registered = true
			}
		}
		if registered {
			out = append(out, socialSetupStep{ID: "callback_ticked", State: "ok", Title: "Callback for " + domain + " marked as registered"})
		} else {
			out = append(out, socialSetupStep{ID: "callback_ticked", State: "todo",
				Title:  "Mark the callback for " + domain + " as registered",
				Detail: "After adding " + redirect + " to the OAuth client in Google Cloud, tick it under Callback URLs below."})
		}
	}

	// 5. What only Google can confirm.
	if redirect != "" {
		out = append(out, socialSetupStep{ID: "google_redirect", State: "manual",
			Title:  "Add the redirect URI to the OAuth client in Google Cloud",
			Detail: "Authorized redirect URIs must contain exactly: " + redirect + ". OneGate cannot check this; Google answers redirect_uri_mismatch if it is missing."})
	}
	out = append(out,
		socialSetupStep{ID: "google_domain", State: "manual",
			Title: "Verify your domain with Google",
			Detail: "In Google Cloud Console open Google Auth Platform, then Branding, and add your domain under Authorized domains. Google requires " +
				"the same Google account to own the domain in Search Console. OneGate cannot check this."},
		socialSetupStep{ID: "google_publish", State: "manual",
			Title: "Publish the consent screen",
			Detail: "An app left in Testing is limited to 100 named test users, and Clients see an unverified-app warning. " +
				"Publish it (In production) for general use. OneGate cannot check this."},
		socialSetupStep{ID: "google_browser", State: "ok",
			Title: "Clients in a phone's sign-in sheet are sent to their normal browser",
			Detail: "Google refuses sign-in inside the sheet a phone opens by itself on joining Wi-Fi. The portal detects that sheet and " +
				"shows the portal address to open in the browser instead; other sign-in methods stay available there."},
	)
	return out
}
