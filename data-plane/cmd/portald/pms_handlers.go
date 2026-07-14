package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// ---- /auth/pms/verify ------------------------------------------------------

type pmsIn struct {
	Room              string `json:"room"`
	FirstName         string `json:"first_name,omitempty"`
	LastName          string `json:"last_name,omitempty"`
	ReservationNumber string `json:"reservation_number,omitempty"`
}

func (h *handler) authPMS(w http.ResponseWriter, r *http.Request) {
	var in pmsIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, 400, "bad body")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		jsonErr(w, 400, "bad ip")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		jsonErr(w, 400, "device not on guest network")
		return
	}
	body, _ := json.Marshal(map[string]string{
		"room":               in.Room,
		"first_name":         in.FirstName,
		"last_name":          in.LastName,
		"reservation_number": in.ReservationNumber,
		"ip":                 ipString(ip),
		"mac":                mac.String(),
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/auth/pms/verify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd pms verify", "err", err)
		jsonErr(w, 502, "service unavailable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
