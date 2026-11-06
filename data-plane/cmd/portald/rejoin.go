package main

// Automatic reconnection (scd phase3_rejoin.go). A device the network disconnected reaches the portal again through its own
// captive-portal probe; before the sign-in form or the probe's "sign in" answer, scd is asked to put it back on the room package
// it already holds. When it is, the probe gets the OS's own "online" answer and the page goes to the connected screen, so the
// guest types nothing. The device is the connection's (kernel neighbour table), never anything the guest sent.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// rejoinBudget bounds the wait for scd, which itself waits for netd to enforce the session.
const rejoinBudget = 6 * time.Second

// tryRejoin reports whether this device is online again on its own package. A device scd found nothing for is not asked again
// for a few seconds, so probes that retry do not each reach scd.
func (h *handler) tryRejoin(r *http.Request) bool {
	ip := clientIP(r)
	if ip == nil || h.scd == nil {
		return false
	}
	key := ip.String()
	if h.rejoinQuiet.quiet(key) {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), rejoinBudget)
	defer cancel()
	mac, ok := h.deviceMAC(ctx, ip)
	if !ok {
		h.rejoinQuiet.remember(key)
		return false
	}
	body, _ := json.Marshal(map[string]any{"device": map[string]string{"ip": ip.String(), "mac": mac.String()}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/phase3/auth/pms/rejoin", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		h.rejoinQuiet.remember(key)
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Outcome string `json:"outcome"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<12)).Decode(&out) != nil || out.Outcome != "REJOINED" {
		h.rejoinQuiet.remember(key)
		return false
	}
	return true
}

// Each OS's own "this network is online" answer, served once the device is reconnected so it leaves the captive state by itself.
const appleOnlinePage = "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>"

// answerRejoinedProbe serves an OS captive probe its "online" answer when the device was just put back on its own package. The
// HTTP front calls it before redirecting a probe (to the HTTPS portal or the portal address): a device that only notifies,
// like Android, would otherwise stay offline until the guest opened the page.
func (h *handler) answerRejoinedProbe(w http.ResponseWriter, r *http.Request) bool {
	if !probePaths[r.URL.Path] || (r.Method != http.MethodGet && r.Method != http.MethodHead) || !h.tryRejoin(r) {
		return false
	}
	writeProbeOnline(w, r.URL.Path)
	return true
}

func writeProbeOnline(w http.ResponseWriter, path string) {
	switch path {
	case "/hotspot-detect.html", "/library/test/success.html":
		writeOnline(w, "text/html", appleOnlinePage, http.StatusOK)
	case "/generate_204", "/gen_204":
		writeOnline(w, "", "", http.StatusNoContent)
	case "/ncsi.txt":
		writeOnline(w, "text/plain", "Microsoft NCSI", http.StatusOK)
	default:
		writeOnline(w, "text/plain", "Microsoft Connect Test", http.StatusOK)
	}
}

func writeOnline(w http.ResponseWriter, contentType, body string, code int) {
	w.Header().Set("Cache-Control", "no-store")
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(code)
	if body != "" {
		_, _ = io.WriteString(w, body)
	}
}
