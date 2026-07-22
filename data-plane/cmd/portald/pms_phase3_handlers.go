package main

// THE GUEST-FACING PHASE-3 PMS FLOW.
//
// Two things make this handler different from the legacy /auth/pms/verify hop:
//
//  1. The guest's identity is derived HERE, from the connection and the appliance's own neighbour table, and
//     is never accepted from the body. The page cannot ask for another device's access because it cannot name
//     another device.
//  2. Every non-success is the SAME answer (see pms_phase3.go). Not the same "kind" of answer — the same
//     bytes. A guest whose room was wrong, whose stay is ambiguous, whose PMS is unreachable, or who is being
//     throttled cannot tell which happened, so the portal is not an occupancy oracle.
//
// The flow itself is resolve → grant, with the guest choosing among SERVER-OFFERED packages in between. When
// exactly one package is offered the choice is made for them, because presenting a single option is not a
// choice — it is a second round trip on the guest's worst network.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// scdResolve/scdGrant mirror scd's Phase-3 contract. They are separate types from the guest-facing ones on
// purpose: what scd tells portald (outcomes, offers, context ids) is strictly more than what the guest is
// told, and one shared struct is how the extra fields eventually leak out.
type scdOffer struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
}

type scdResolveResp struct {
	Outcome       string     `json:"outcome"`
	AuthContextID string     `json:"auth_context_id"`
	ExpiresIn     int        `json:"expires_in_seconds"`
	Offers        []scdOffer `json:"offers"`
}

type scdGrantResp struct {
	Outcome       string `json:"outcome"`
	SessionID     string `json:"session_id"`
	EntitlementID string `json:"entitlement_id"`
}

// phase3In is the guest's submission. Note what is NOT here: no ip, no mac, no stay, no interface, no price.
type phase3In struct {
	Room              string `json:"room"`
	LastName          string `json:"last_name,omitempty"`
	ReservationNumber string `json:"reservation_number,omitempty"`
	// RequestID makes a retry idempotent: the same attempt resolved twice records one resolution, not two.
	RequestID string `json:"request_id"`
	// AuthContextID + PackageRevisionID are present only on the SECOND call, when the guest picked among
	// several offers. On the first call they are empty and the portal resolves first.
	AuthContextID     string `json:"auth_context_id,omitempty"`
	PackageRevisionID string `json:"package_revision_id,omitempty"`
}

// phase3Out is the guest-facing body. On success it names their own session; on failure it is the uniform
// message and nothing else. `choices` appears only when the guest genuinely has to pick.
type phase3Out struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`

	SessionID  string `json:"session_id,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`

	// NeedsChoice + Choices are a SUCCESSFUL identity proof awaiting a package selection. They carry no stay,
	// interface or property detail — only what the guest is being asked to choose between.
	NeedsChoice   bool           `json:"needs_choice,omitempty"`
	AuthContextID string         `json:"auth_context_id,omitempty"`
	Choices       []phase3Choice `json:"choices,omitempty"`
}

type phase3Choice struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
}

// authPMSPhase3 serves POST /auth/pms/phase3.
func (h *handler) authPMSPhase3(w http.ResponseWriter, r *http.Request) {
	var in phase3In
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		h.phase3Fail(w, "malformed_request")
		return
	}
	// IDENTITY, derived here and nowhere else.
	ip := clientIP(r)
	if ip == nil {
		h.phase3Fail(w, "no_source_address")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		h.phase3Fail(w, "device_not_on_guest_network")
		return
	}
	device := map[string]string{"ip": ipString(ip), "mac": mac.String()}

	// SECOND CALL: the guest already proved who they are and has now chosen a package.
	if strings.TrimSpace(in.AuthContextID) != "" {
		h.phase3Grant(w, r, in.AuthContextID, in.PackageRevisionID, device)
		return
	}

	res, ok := h.phase3Resolve(w, r, in, device)
	if !ok {
		return // the uniform failure has already been written
	}
	switch len(res.Offers) {
	case 0:
		// A verified guest with nothing they may be granted is a configuration problem, not an identity one —
		// but the guest cannot act on that difference, and an empty "choose your package" step is a dead end
		// that looks like the portal is broken. It collapses to the uniform answer like everything else.
		h.phase3Fail(w, "verified_without_offers")
	case 1:
		h.phase3Grant(w, r, res.AuthContextID, res.Offers[0].PackageRevisionID, device)
	default:
		choices := make([]phase3Choice, 0, len(res.Offers))
		for _, o := range res.Offers {
			choices = append(choices, phase3Choice{
				PackageRevisionID: o.PackageRevisionID, Code: o.Code, DownKbps: o.DownKbps, UpKbps: o.UpKbps})
		}
		writeJSONPortal(w, http.StatusOK, phase3Out{
			OK: true, NeedsChoice: true, AuthContextID: res.AuthContextID, Choices: choices})
	}
}

func (h *handler) phase3Resolve(w http.ResponseWriter, r *http.Request, in phase3In, device map[string]string) (scdResolveResp, bool) {
	body, _ := json.Marshal(map[string]any{
		"room":               in.Room,
		"last_name":          in.LastName,
		"reservation_number": in.ReservationNumber,
		"request_id":         in.RequestID,
		"device":             device,
	})
	var out scdResolveResp
	if !h.scdPhase3Call(r, "http://unix/v1/phase3/auth/pms/resolve", body, &out) {
		h.phase3Fail(w, "scd_unavailable")
		return out, false
	}
	if out.Outcome != "VERIFIED" || out.AuthContextID == "" {
		// Every resolver outcome that is not a clean single match collapses to the same guest answer here.
		h.phase3Fail(w, "not_verified")
		return out, false
	}
	return out, true
}

func (h *handler) phase3Grant(w http.ResponseWriter, r *http.Request, authContextID, packageRevID string, device map[string]string) {
	body, _ := json.Marshal(map[string]any{
		"auth_context_id":     authContextID,
		"package_revision_id": packageRevID,
		"device":              device,
	})
	var out scdGrantResp
	if !h.scdPhase3Call(r, "http://unix/v1/phase3/auth/pms/grant", body, &out) {
		h.phase3Fail(w, "scd_unavailable")
		return
	}
	if out.Outcome != "VERIFIED" || out.SessionID == "" {
		// A grant that did not produce a session produced NO access. Reporting success here would leave the
		// guest staring at a "you're connected" page on a network that will not carry their traffic.
		h.phase3Fail(w, "grant_refused")
		return
	}
	writeJSONPortal(w, http.StatusOK, phase3Out{OK: true, SessionID: out.SessionID, RedirectTo: "/success"})
}

// scdPhase3Call performs one internal hop. A transport failure and a refusal are both handled by the caller
// as the same uniform guest answer; the distinction only reaches the log.
func (h *handler) scdPhase3Call(r *http.Request, url string, body []byte, out any) bool {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("phase3 scd hop failed", "url", url, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		slog.Warn("phase3 scd hop refused", "url", url, "status", resp.StatusCode)
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// phase3Fail writes THE uniform non-success — through the SAME builder the legacy PMS path uses. Reusing it
// is the point: two functions that each "write the uniform failure" are two places that can drift, and the
// drift would only ever be discovered by an attacker noticing the difference.
func (h *handler) phase3Fail(w http.ResponseWriter, reason string) {
	// Every internal cause collapses to one outcome here. The real reason is logged and, for a resolution,
	// already recorded durably by scd.
	status, body, audit := buildGuestPMSResponse(outcomeNoMatch, reason, "", "")
	slog.Info("phase3 guest auth not verified", "reason", audit.ReasonCode)
	writeGuestPMSResponse(w, status, body)
}

func writeJSONPortal(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
