package pmsprovider

import "testing"

// Audit AD-40: a REST PMS base URL may not name the appliance itself or a link-local address; RFC 1918 stays allowed.
func TestRESTHostsLoopbackAndLinkLocalAreRefusedPrivateAllowed(t *testing.T) {
	refused := []string{
		"https://127.0.0.1/api", "https://127.1.2.3:8443/x", "https://localhost/api", "https://LOCALHOST./api",
		"https://pms.localhost/api", "https://[::1]/api", "https://169.254.169.254/latest", "https://[fe80::1]/api",
		"https://0.0.0.0/api", "https://[::ffff:127.0.0.1]/api", "https://[::]/api",
	}
	for _, u := range refused {
		if validHTTPSURL(u) == "" {
			t.Errorf("%s must be refused", u)
		}
	}
	allowed := []string{
		"https://10.20.30.40/api", "https://172.16.5.9:8443/api", "https://192.168.1.50/api", "https://pms.hotel.example/api",
		"https://api.mews.com/api/connector/v1", "https://8.8.8.8/x",
	}
	for _, u := range allowed {
		if msg := validHTTPSURL(u); msg != "" {
			t.Errorf("%s must be allowed (on-premises or public), got %q", u, msg)
		}
	}
}
