package main

// Two resource keys (auth.go): code-requests (list recent SMS/WhatsApp code requests) and code-reveal (read a
// live code to the client it was sent to). The reveal takes step-up and a reason; the code is never written to
// the audit record, log or any other response. scd owns the sealed copy; edged never holds the key.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

var codeChallengeRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *server) codeRequestsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listCodeRequests)
	return r
}

func (s *server) codeRevealRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{id}", s.revealCode)
	return r
}

func (s *server) relayScd(w http.ResponseWriter, st int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// The legacy path is tried when an older scd answers 404 on the current one.
const (
	scdCodeRequestsPath       = "/v1/admin/code-requests"
	scdCodeRequestsLegacyPath = "/v1/admin/temporary-access"
)

func (s *server) listCodeRequests(w http.ResponseWriter, r *http.Request) {
	query := ""
	if h := r.URL.Query().Get("hours"); h != "" {
		query = "?hours=" + url.QueryEscape(h)
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodGet, scdCodeRequestsPath+query, nil)
	if err == nil && st == http.StatusNotFound {
		st, raw, err = s.scd.call(r.Context(), http.MethodGet, scdCodeRequestsLegacyPath+query, nil)
	}
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return
	}
	// The full address and device MAC are shown only to a role that may also reveal a code; other readers see them masked.
	if st == http.StatusOK {
		var roles []string
		if sess := sessFrom(r.Context()); sess != nil {
			roles = sess.Roles
		}
		if !permFor(roles, "code-reveal", permWrite) {
			raw = maskCodeRequests(raw)
		}
	}
	s.relayScd(w, st, raw)
}

// maskCodeRequests fails closed: an unparseable body becomes an empty list, never relayed unmasked.
func maskCodeRequests(raw []byte) []byte {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return []byte(`{"requests":[]}`)
	}
	reqs, _ := body["requests"].([]any)
	for _, x := range reqs {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		dest, _ := m["destination"].(string)
		ft := "PHONE"
		if ch, _ := m["channel"].(string); ch == "email" {
			ft = "EMAIL"
		}
		m["destination"] = iamv2.MaskIdentity(iamv2.FactorClaim{Type: ft, Value: dest})
		if mac, _ := m["device_mac"].(string); len(mac) == 17 {
			m["device_mac"] = "••:••:••:••:" + mac[12:]
		}
		m["masked"] = true
	}
	out, err := json.Marshal(body)
	if err != nil {
		return []byte(`{"requests":[]}`)
	}
	return out
}

func (s *server) revealCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > 500 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a reason is required")
		return
	}
	id := chi.URLParam(r, "id")
	if !codeChallengeRE.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad id")
		return
	}
	actor, ok := s.paymentStepUp(w, r, in.Password)
	if !ok {
		return
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/otp/"+id+"/reveal", map[string]any{"operator": actor})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the sign-in service could not be reached")
		return
	}
	if st < 300 {
		// Never audit the code itself.
		s.audit(r, "otp_code.revealed", "otp_challenge", id, map[string]any{"reason": reason})
	}
	s.relayScd(w, st, raw)
}
