package main

// CHOOSE A DEVICE TO REPLACE (docs/architecture/ONEGATE_DEVICE_MANAGEMENT.md, migration 0118).
//
// A client signs in on a new device, and the package they hold already has all its devices in use. Until now the
// answer was a sentence -- "Disconnect another device and try again" -- with nothing to disconnect it with: the
// slot of a device that had signed out was still held, so the client was stuck. This page lists the package's
// devices and lets the client choose the one this device replaces. scd takes that device off the network and moves
// its slot to this one in one transaction (client_devices.go); the package's time, data and owner do not change.
//
// The proof is the client's fresh sign-in on THIS device (the commerce session's auth context, which never leaves
// the server); the only thing the browser sends is which of the listed devices to replace.
//
//	GET  /devices/replace   the choice
//	POST /devices/replace   device_id=<one of the listed ids>

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type replaceRow struct {
	ID       string
	Name     string
	Online   bool
	LastSeen string // RFC 3339, rendered by the page's script in the device's own clock
}

type replaceView struct {
	guestPage
	Lead     string
	Slots    string
	Changes  string
	CanSwap  bool
	Error    string
	Devices  []replaceRow
	Identity string
}

func fillWord(s, key, val string) string { return strings.ReplaceAll(s, "{"+key+"}", val) }

// scdDevices calls one of scd's client device routes over the trusted socket.
func (h *handler) scdDevices(r *http.Request, path string, body map[string]string) (scdDeviceResp, bool) {
	var out scdDeviceResp
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://unix"+path, bytes.NewReader(raw))
	if err != nil {
		return out, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.scd.Do(req)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return out, json.Unmarshal(b, &out) == nil
}

// replaceDeviceIdentity is the request's device and fresh sign-in, or a refusal already rendered.
func (h *handler) replaceDeviceIdentity(w http.ResponseWriter, r *http.Request) (commerceSession, map[string]string, bool) {
	sess, ok := h.resolveCommerceSession(r)
	if !ok {
		h.landing(w, r, "Please sign in.")
		return sess, nil, false
	}
	ip := clientIP(r)
	if ip == nil {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return sess, nil, false
	}
	mac, ok := h.deviceMAC(r.Context(), ip)
	if !ok {
		h.landing(w, r, "Your device isn't connected to this Wi-Fi network.")
		return sess, nil, false
	}
	return sess, map[string]string{"ip": ipString(ip), "mac": mac.String(), "auth_context_id": sess.authContextID}, true
}

// replacePage serves GET /devices/replace.
func (h *handler) replacePage(w http.ResponseWriter, r *http.Request) {
	sess, dev, ok := h.replaceDeviceIdentity(w, r)
	if !ok {
		return
	}
	h.renderReplace(w, r, sess, dev, "")
}

func (h *handler) renderReplace(w http.ResponseWriter, r *http.Request, sess commerceSession, dev map[string]string, errKey string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	v := replaceView{guestPage: p, Identity: sess.identityLabel}
	list, ok := h.scdDevices(r, "/v1/devices/list", dev)
	if !ok || list.Outcome != "LISTED" || len(list.Devices) == 0 {
		v.Error = p.T["rep.unavailable"]
		_ = replaceTmpl.Execute(w, v)
		return
	}
	max := strconv.Itoa(list.Limit)
	v.Lead = fillWord(p.T["rep.lead"], "max", max)
	v.Slots = fillWord(fillWord(p.T["dev.slots"], "n", strconv.Itoa(list.InUse)), "max", max)
	v.CanSwap = list.ChangesLeft > 0
	if v.CanSwap {
		v.Changes = fillWord(p.T["dev.changes"], "n", strconv.Itoa(list.ChangesLeft))
	} else {
		v.Changes = p.T["dev.changes.none"]
	}
	if errKey != "" {
		v.Error = p.T[errKey]
	}
	n := 0
	for _, d := range list.Devices {
		if d.ThisDevice {
			continue
		}
		n++
		v.Devices = append(v.Devices, replaceRow{ID: d.ID, Name: fillWord(p.T["dev.name"], "n", strconv.Itoa(n)),
			Online: d.Online, LastSeen: d.LastSeen})
	}
	_ = replaceTmpl.Execute(w, v)
}

// replaceDevice serves POST /devices/replace.
func (h *handler) replaceDevice(w http.ResponseWriter, r *http.Request) {
	sess, dev, ok := h.replaceDeviceIdentity(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	}
	target := strings.TrimSpace(r.PostFormValue("device_id"))
	if target == "" || len(target) > 64 {
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	}
	body := map[string]string{"ip": dev["ip"], "mac": dev["mac"], "auth_context_id": dev["auth_context_id"], "device_id": target}
	out, ok := h.scdDevices(r, "/v1/devices/replace", body)
	switch {
	case ok && (out.Outcome == "REPLACED" || out.Outcome == "SLOT_FREE") && out.EntitlementID != "":
	case ok && out.Outcome == "LIMIT_TODAY":
		h.renderReplace(w, r, sess, dev, "dev.changes.none")
		return
	default:
		slog.Info("device replace not served", "outcome", out.Outcome)
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	}
	sid, failure := h.activateEnforced(r, sess, out.EntitlementID)
	switch failure {
	case "":
	case activateVerification:
		h.renderAccountPage(w, r, nil, true, accountNote{})
		return
	case activateDeviceLimit:
		// Another device took the place in the moment between: choose again.
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	default:
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	}
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.acquired = true
		h.commerceSessions.put(c.Value, sess)
	}
	http.Redirect(w, r, "/success?s="+url.QueryEscape(sid), http.StatusSeeOther)
}

// toReplace sends a client refused at the device limit to the choice, keeping their sign-in for it. token is the
// commerce session's cookie value when it is being issued in this same response ("" when the cookie exists).
func (h *handler) toReplace(w http.ResponseWriter, r *http.Request, sess commerceSession, token string, asJSON bool) {
	if token != "" && h.commerceSessions != nil {
		h.commerceSessions.put(token, sess)
		http.SetCookie(w, &http.Cookie{Name: commerceCookie, Value: token, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(commerceSessionTTL)})
	}
	if asJSON {
		writeJSONPortal(w, http.StatusOK, map[string]any{"next": "/devices/replace"})
		return
	}
	http.Redirect(w, r, "/devices/replace", http.StatusSeeOther)
}

var replaceTmpl = template.Must(template.New("replace").Parse(compactMarkup(replaceHTML)))

const replaceHTML = guestHead + `
<title>{{index .T "rep.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title">{{index .T "rep.title"}}</h1>
  {{with .Lead}}<p class="page-lead">{{.}}</p>{{end}}
  {{with .Error}}<div class="notice show" role="alert">{{.}}</div>{{end}}
  {{with .Slots}}<p class="page-lead">{{.}} · {{$.Changes}}</p>{{end}}
  <div class="choice-list">
  {{range .Devices}}<form method="post" action="/devices/replace">
    <input type="hidden" name="device_id" value="{{.ID}}">
    <button class="choice" type="submit"{{if not $.CanSwap}} disabled{{end}}><span class="c-text"><span class="c-name">{{.Name}}</span><span class="c-detail">{{if .Online}}{{index $.T "dev.online"}}{{else}}{{index $.T "dev.offline"}}{{end}}{{if .LastSeen}} · <span class="dev-seen" data-at="{{.LastSeen}}"></span>{{end}}</span><span class="c-detail c-price">{{index $.T "rep.btn"}}</span></span>` + iconChevNext + `</button>
  </form>{{end}}
  </div>
  <form method="post" action="/logout"><button class="link" type="submit">{{index .T "pkg.signout"}}</button></form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
  // "Last used", in the device's own clock and language.
  (function () {
    var els = document.querySelectorAll('.dev-seen');
    for (var i = 0; i < els.length; i++) {
      var ts = Date.parse(els[i].getAttribute('data-at'));
      if (!isNaN(ts)) els[i].textContent = new Date(ts).toLocaleString();
    }
    var forms = document.querySelectorAll('.choice-list form');
    for (var k = 0; k < forms.length; k++) {
      forms[k].addEventListener('submit', function () {
        setTimeout(function () {
          var bs = document.querySelectorAll('.choice-list button');
          for (var j = 0; j < bs.length; j++) bs[j].disabled = true;
        }, 0);
      });
    }
  })();
</script>
</body></html>`
