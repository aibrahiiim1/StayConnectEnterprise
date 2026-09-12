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
	Outcome string `json:"outcome"`
	// FailureClass is the coarse class scd computed for a non-success. It is forwarded into the guest's
	// message and nowhere else; the exact reason stays in scd and in the attempt record.
	FailureClass string `json:"failure_class"`
	// RetryAfterSeconds is the server's remaining restriction time on a rate-limited refusal. portald passes
	// it through to the guest untouched; it never computes, extends or shortens it.
	RetryAfterSeconds int        `json:"retry_after_seconds"`
	AuthContextID     string     `json:"auth_context_id"`
	ExpiresIn         int        `json:"expires_in_seconds"`
	Offers            []scdOffer `json:"offers"`
}

type scdGrantResp struct {
	Outcome       string `json:"outcome"`
	FailureClass  string `json:"failure_class"`
	SessionID     string `json:"session_id"`
	EntitlementID string `json:"entitlement_id"`
}

// The two classes portald may decide on its own. Everything else comes from scd: portald knows what it could
// not do, never what the guest got wrong, and inventing a credential verdict here would be guessing.
const (
	classCredential = "CREDENTIAL"
	classTechnical  = "TECHNICAL"
	// classPostStay selects the wording the post-stay PIN path has always used. It is not a fourth guest
	// class — it is the same "we could not verify" sentence, kept for a method whose guests have no room
	// number or surname to be told to re-check.
	classPostStay = "POST_STAY"
)

// phase3In is the guest's submission. Note what is NOT here: no ip, no mac, no stay, no interface, no price.
//
// EVERY VERIFICATION FIELD THE PORTAL CAN SEND MUST HAVE A HOME HERE. This struct is the whole of what
// survives the hop to scd: a field the browser sends and this type does not name is decoded into nothing and
// forwarded as nothing, silently. scd then sees a room with no evidence beside it and answers
// `incomplete_evidence`, which reaches the guest as the uniform failure — indistinguishable from a wrong
// surname.
//
// That is exactly what happened. `verification` (room_any) and `first_name` (room_firstname) were added to
// the portal script and to scd, and this struct was not updated, so both modes were dead on the real guest
// path while every test that called scd directly passed. Two of the four sign-in modes could never work,
// and nothing said so.
type phase3In struct {
	Room string `json:"room"`
	// Verification is the ONE value a guest types under room_any, where the portal does not ask which kind of
	// identifier it is. It is forwarded verbatim; scd compares it against first name, last name and
	// reservation number together. Nothing here inspects its shape — guessing "digits mean a reservation
	// number" is the legacy behaviour room_any exists to remove.
	Verification      string `json:"verification,omitempty"`
	FirstName         string `json:"first_name,omitempty"`
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
	// RetryAfterSeconds is the server's remaining restriction time, present only on a restricted refusal. The
	// page counts it down so the guest watches the wait shrink; it never decides when the wait is over.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`

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
	// The budget starts HERE, before the body is even read, so the offset every non-success leaves at is
	// measured from the guest's arrival. Starting it after the cheap local checks would leave exactly those
	// checks — malformed body, unknown device — distinguishable by how early they answer.
	b := h.newPhase3Budget(r)
	defer b.cancel()

	var in phase3In
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		h.phase3Fail(w, r, b, "malformed_request", classCredential)
		return
	}
	// IDENTITY, derived here and nowhere else.
	//
	// THESE TWO ARE TECHNICAL, and the distinction is not cosmetic. A device the appliance cannot place on a
	// guest network has a networking problem, not a typing problem; telling that guest to re-read their
	// surname would send them round a loop they cannot escape, because nothing they type will ever help.
	ip := clientIP(r)
	if ip == nil {
		h.phase3Fail(w, r, b, "no_source_address", classTechnical)
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		h.phase3Fail(w, r, b, "device_not_on_guest_network", classTechnical)
		return
	}
	device := map[string]string{"ip": ipString(ip), "mac": mac.String()}

	// SECOND CALL: the guest already proved who they are and has now chosen a package.
	if strings.TrimSpace(in.AuthContextID) != "" {
		h.phase3Grant(w, r, b, in.AuthContextID, in.PackageRevisionID, device)
		return
	}

	res, ok := h.phase3Resolve(w, r, b, in, device)
	if !ok {
		return // the uniform failure has already been written
	}
	switch len(res.Offers) {
	case 0:
		// A verified guest with nothing they may be granted is a configuration problem, not an identity one.
		// It is TECHNICAL for the same reason scd records it as its own result: their details were right, and
		// "check your details" is advice that cannot help them.
		h.phase3Fail(w, r, b, "verified_without_offers", classTechnical)
	case 1:
		h.phase3Grant(w, r, b, res.AuthContextID, res.Offers[0].PackageRevisionID, device)
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

func (h *handler) phase3Resolve(w http.ResponseWriter, r *http.Request, b *phase3Budget, in phase3In, device map[string]string) (scdResolveResp, bool) {
	// Every verification field is forwarded, and the guest-supplied identity stops here. `device` is built by
	// the appliance from its own neighbour table — the guest cannot supply ip, mac, stay or interface, and no
	// field of phase3In can reach those keys.
	body, _ := json.Marshal(map[string]any{
		"room":               in.Room,
		"verification":       in.Verification,
		"first_name":         in.FirstName,
		"last_name":          in.LastName,
		"reservation_number": in.ReservationNumber,
		"request_id":         in.RequestID,
		"device":             device,
	})
	var out scdResolveResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/auth/pms/resolve", body, &out) {
		// This covers an abandoned hop as well as a refused one: when the budget expires mid-flight the hop
		// returns a context error and lands here. scd said nothing, so nothing about what the guest typed is
		// known — TECHNICAL is the only honest class.
		h.phase3Fail(w, r, b, "scd_unavailable", classTechnical)
		return out, false
	}
	if out.Outcome != "VERIFIED" || out.AuthContextID == "" {
		// scd decided. Its class crosses to the guest verbatim; portald deliberately re-derives nothing —
		// including the remaining wait, which is the server's own number and is forwarded as it arrived.
		h.phase3FailRetryAfter(w, r, b, "not_verified", out.FailureClass, out.RetryAfterSeconds)
		return out, false
	}
	return out, true
}

func (h *handler) phase3Grant(w http.ResponseWriter, r *http.Request, b *phase3Budget, authContextID, packageRevID string, device map[string]string) {
	body, _ := json.Marshal(map[string]any{
		"auth_context_id":     authContextID,
		"package_revision_id": packageRevID,
		"device":              device,
	})
	var out scdGrantResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/auth/pms/grant", body, &out) {
		h.phase3Fail(w, r, b, "scd_unavailable", classTechnical)
		return
	}
	if out.Outcome != "VERIFIED" || out.SessionID == "" {
		// A grant that did not produce a session produced NO access. Reporting success here would leave the
		// guest staring at a "you are connected" page on a network that will not carry their traffic. The
		// guest already proved who they are, so the class comes from scd rather than being assumed.
		h.phase3Fail(w, r, b, "grant_refused", out.FailureClass)
		return
	}
	writeJSONPortal(w, http.StatusOK, phase3Out{OK: true, SessionID: out.SessionID, RedirectTo: "/success"})
}

// scdPhase3Call performs one internal hop, under the BUDGET's context rather than the request's. That is what
// makes the budget a ceiling: a hop that has not answered when the budget expires is cut off there instead of
// running on to the 5s client timeout and handing the guest a distinguishably slow refusal.
//
// A transport failure, an abandonment and a refusal are all handled by the caller as the same uniform guest
// answer; the distinction only reaches the log.
func (h *handler) scdPhase3Call(b *phase3Budget, url string, body []byte, out any) bool {
	req, err := http.NewRequestWithContext(b.ctx, http.MethodPost, url, bytes.NewReader(body))
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
func (h *handler) phase3Fail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason, class string) {
	h.phase3FailRetryAfter(w, r, b, reason, class, 0)
}

// phase3FailRetryAfter is the same refusal carrying the server's remaining restriction time. Every other
// failure goes through phase3Fail and therefore cannot accidentally acquire a retry hint.
func (h *handler) phase3FailRetryAfter(w http.ResponseWriter, r *http.Request, b *phase3Budget,
	reason, class string, retryAfter int) {
	// The internal cause stays here; only the coarse class crosses to the guest. `class` is scd's when scd
	// answered, and portald's own judgement when it did not get that far — see the call sites.
	status, body, audit := buildGuestPMSResponse(outcomeNoMatch, reason, class, "", "", retryAfter)
	slog.Info("phase3 guest auth not verified", "reason", audit.ReasonCode, "class", class)
	// The wait happens BEFORE the write, and before the log line is of any use to the guest. Waiting after
	// writing would be indistinguishable from not waiting at all: the bytes are already on the wire, and the
	// clock the attacker reads stops when they arrive.
	b.wait(r)
	writeGuestPMSResponse(w, status, body)
}

func writeJSONPortal(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
