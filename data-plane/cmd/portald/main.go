// portald — captive portal for StayConnect.
//
// Listens on :8380 (HTTP) and :8343 (HTTPS). nftables DNATs unauthenticated
// guest HTTP/HTTPS traffic here. On POST /auth/voucher we ask scd (over its
// Unix socket) to authorize the client; scd updates the nft set and the DB.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

type cfg struct {
	HTTPAddr   string
	HTTPSAddr  string
	CertFile   string
	KeyFile    string
	ScdSocket  string
	PortalFQDN string
}

func loadCfg() cfg {
	return cfg{
		HTTPAddr:   envOr("PORTALD_HTTP_ADDR", ":8380"),
		HTTPSAddr:  envOr("PORTALD_HTTPS_ADDR", ":8343"),
		CertFile:   envOr("PORTALD_CERT", "/etc/stayconnect/tls/portal.crt"),
		KeyFile:    envOr("PORTALD_KEY", "/etc/stayconnect/tls/portal.key"),
		ScdSocket:  envOr("PORTALD_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		PortalFQDN: envOr("PORTALD_FQDN", "portal.stayconnect.local"),
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type handler struct {
	cfg      cfg
	scd      *http.Client
	tmplLand *template.Template
	tmplSucc *template.Template
	arpCache arpLookup
}

func newHandler(c cfg) (*handler, error) {
	tland, err := template.New("land").Parse(landingHTML)
	if err != nil {
		return nil, err
	}
	tsucc, err := template.New("succ").Parse(successHTML)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.ScdSocket)
		},
	}
	return &handler{
		cfg:      c,
		scd:      &http.Client{Transport: tr, Timeout: 5 * time.Second},
		tmplLand: tland,
		tmplSucc: tsucc,
		arpCache: defaultArp,
	}, nil
}

// clientIP extracts the real IP from the connection. nftables DNAT preserves
// the original source address, so RemoteAddr is authoritative on this path.
func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func (h *handler) landing(w http.ResponseWriter, r *http.Request, errMsg string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmplLand.Execute(w, map[string]any{"Error": errMsg})
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	h.landing(w, r, "")
}

func (h *handler) authVoucher(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	code := strings.TrimSpace(strings.ToUpper(r.FormValue("code")))
	if code == "" {
		h.landing(w, r, "Please enter a voucher code.")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "Unable to detect your device address.")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		slog.Warn("no arp entry", "ip", ip.String())
		h.landing(w, r, "Your device isn't on the guest network.")
		return
	}

	body, _ := json.Marshal(map[string]string{
		"ip":      ip.String(),
		"mac":     mac.String(),
		"voucher": code,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd call", "err", err)
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		msg := "Invalid voucher."
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &e) == nil && e.Error != "" {
			msg = "Voucher " + e.Error + "."
		}
		h.landing(w, r, msg)
		return
	}
	var ok2 struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	_ = json.Unmarshal(payload, &ok2)
	http.Redirect(w, r, "/success?s="+ok2.SessionID+"&t="+fmt.Sprint(ok2.DurationSeconds), http.StatusSeeOther)
}

func (h *handler) authCredentials(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.landing(w, r, "Bad request.")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		h.landing(w, r, "Please enter your username and password.")
		return
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "Unable to detect your device address.")
		return
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		h.landing(w, r, "Your device isn't on the guest network.")
		return
	}
	body, _ := json.Marshal(map[string]string{
		"ip": ip.String(), "mac": mac.String(), "username": username, "password": password,
	})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		"http://unix/v1/sessions/authorize-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		slog.Error("scd authorize-credentials", "err", err)
		h.landing(w, r, "Service unavailable. Please try again.")
		return
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		msg := "Invalid username or password."
		if e.Error == "LICENSE_CAPACITY_REACHED" {
			msg = "The guest network is at capacity. Please try again shortly."
		}
		h.landing(w, r, msg)
		return
	}
	var ok2 struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	_ = json.Unmarshal(payload, &ok2)
	http.Redirect(w, r, "/success?s="+ok2.SessionID+"&t="+fmt.Sprint(ok2.DurationSeconds), http.StatusSeeOther)
}

func (h *handler) success(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	dur, _ := time.ParseDuration(r.URL.Query().Get("t") + "s")
	_ = h.tmplSucc.Execute(w, map[string]any{
		"SessionID":       r.URL.Query().Get("s"),
		"DurationSeconds": int(dur.Seconds()),
		"HumanRemaining":  humanDuration(dur),
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ip == nil {
		http.Error(w, "bad ip", 400)
		return
	}
	body, _ := json.Marshal(map[string]string{"ip": ip.String(), "reason": "admin"})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "http://unix/v1/sessions/revoke", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	_, _ = h.scd.Do(req)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ip == nil {
		http.Error(w, "bad ip", 400)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://unix/v1/sessions/status?ip="+ip.String(), nil)
	resp, err := h.scd.Do(req)
	if err != nil {
		http.Error(w, "scd unreachable", 500)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, resp.Body)
}

// Well-known captive-detect probes.
// Unauth: respond with content that triggers the OS's captive-portal UI.
// Auth (source IP is in nft set; DNAT wouldn't reach us) → N/A here.
func (h *handler) appleProbe(w http.ResponseWriter, r *http.Request) {
	// iOS/macOS expects exactly "Success" HTML to consider the network open.
	// We deliberately DON'T serve that — we want the captive UI to appear.
	h.landing(w, r, "")
}
func (h *handler) googleProbe(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+h.cfg.PortalFQDN+"/", http.StatusFound)
}
func (h *handler) windowsProbe(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+h.cfg.PortalFQDN+"/", http.StatusFound)
}

func (h *handler) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/", h.index)
	r.Post("/auth/voucher", h.authVoucher)
	r.Post("/auth/credentials", h.authCredentials)
	r.Post("/auth/otp/request", h.authOTPRequest)
	r.Post("/auth/otp/verify", h.authOTPVerify)
	r.Get("/auth/social/start", h.socialStart)
	r.Get("/auth/social/callback", h.socialCallback)
	r.Get("/api/oauth/stub/authorize", h.stubAuthorize)
	r.Post("/api/oauth/stub/authorize-confirm", h.stubAuthorizeConfirm)
	r.Post("/auth/pms/verify", h.authPMS)
	r.Get("/api/auth-methods", h.authMethods)
	r.Get("/success", h.success)
	r.Post("/logout", h.logout)
	r.Get("/status", h.status)

	// Captive detect probes (OS-specific well-known paths).
	r.Get("/hotspot-detect.html", h.appleProbe)
	r.Get("/library/test/success.html", h.appleProbe)
	r.Get("/generate_204", h.googleProbe)
	r.Get("/gen_204", h.googleProbe)
	r.Get("/connecttest.txt", h.windowsProbe)
	r.Get("/ncsi.txt", h.windowsProbe)

	// Any other path: show the portal.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) { h.index(w, req) })
	return r
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Adaptive crash-loop backoff (see internal/startupbackoff).
	startupbackoff.Guard("portald")

	c := loadCfg()
	h, err := newHandler(c)
	if err != nil {
		slog.Error("handler init", "err", err)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := h.routes()

	httpSrv := &http.Server{Addr: c.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	httpsSrv := &http.Server{Addr: c.HTTPSAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		slog.Info("portald HTTP", "addr", c.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http serve", "err", err)
		}
	}()
	go func() {
		if _, err := os.Stat(c.CertFile); err != nil {
			slog.Warn("portald HTTPS disabled — cert missing", "cert", c.CertFile)
			return
		}
		slog.Info("portald HTTPS", "addr", c.HTTPSAddr)
		if err := httpsSrv.ListenAndServeTLS(c.CertFile, c.KeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("https serve", "err", err)
		}
	}()

	<-rootCtx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = httpsSrv.Shutdown(shutCtx)
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
