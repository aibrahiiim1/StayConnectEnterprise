package main

import "testing"

func TestSocialRedirectURIMustBeThePortalsHTTPSCallback(t *testing.T) {
	good := "https://portal.stayconnect.local/auth/social/callback"
	if msg := validateSocialRedirectURI(good); msg != "" {
		t.Fatalf("good URI refused: %s", msg)
	}
	for _, bad := range []string{
		"http://portal.stayconnect.local/auth/social/callback",
		"https://portal.stayconnect.local/callback",
		"https://portal.stayconnect.local/auth/social/callback?x=1",
		"https://portal.stayconnect.local/auth/social/callback#f",
		"https://u:p@portal.stayconnect.local/auth/social/callback",
		"/auth/social/callback",
		"not a url",
	} {
		if validateSocialRedirectURI(bad) == "" {
			t.Errorf("accepted %q", bad)
		}
	}
}
