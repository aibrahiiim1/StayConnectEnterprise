package main

// Choose a device to replace when all of the package's devices are in use; scd moves the slot atomically (client_devices.go).
// Proof is the fresh sign-in on this device; the browser sends only the device_id to replace (GET shows the choice, POST replaces).

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
)

type replaceRow struct {
	ID       string
	Name     string
	Online   bool
	LastSeen string
	Label    string
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
	// Devices are numbered by place in the package's list, as on the online page, so "Device 2" matches; this device is skipped, not renumbered around.
	for i, d := range list.Devices {
		if d.ThisDevice {
			continue
		}
		name := fillWord(p.T["dev.name"], "n", strconv.Itoa(i+1))
		v.Devices = append(v.Devices, replaceRow{ID: d.ID, Name: name, Online: d.Online, LastSeen: d.LastSeen,
			// One accessible name per button.
			Label: name + " — " + p.T["rep.btn"]})
	}
	_ = replaceTmpl.Execute(w, v)
}

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
	// One change however many taps: once scd moved a place to this device, a retry activates on that place instead of replacing and disconnecting another device.
	ent := sess.replacedEntitlement
	if ent == "" {
		body := map[string]string{"ip": dev["ip"], "mac": dev["mac"], "auth_context_id": dev["auth_context_id"], "device_id": target}
		out, ok := h.scdDevices(r, "/v1/devices/replace", body)
		switch {
		case ok && (out.Outcome == "REPLACED" || out.Outcome == "SLOT_FREE") && out.EntitlementID != "":
			ent = out.EntitlementID
			sess.replacedEntitlement = ent
			if c, err := r.Cookie(commerceCookie); err == nil && h.commerceSessions != nil {
				h.commerceSessions.put(c.Value, sess)
			}
		case ok && out.Outcome == "LIMIT_TODAY":
			h.renderReplace(w, r, sess, dev, "dev.changes.none")
			return
		default:
			slog.Info("device replace not served", "outcome", out.Outcome)
			h.renderReplace(w, r, sess, dev, "dev.refused")
			return
		}
	}
	sid, failure := h.activateEnforced(r, sess, ent)
	switch failure {
	case "":
	case activateVerification:
		h.renderAccountPage(w, r, nil, true, accountNote{})
		return
	case activateDeviceLimit:
		// Another device took the place in the moment between: this device holds none, so it chooses again.
		h.updateCommerceSession(r, func(s *commerceSession) { s.replacedEntitlement = "" })
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	default:
		h.renderReplace(w, r, sess, dev, "dev.refused")
		return
	}
	if c, err := r.Cookie(commerceCookie); err == nil {
		sess.acquired = true
		sess.replacedEntitlement = ""
		h.commerceSessions.put(c.Value, sess)
	}
	http.Redirect(w, r, "/success?s="+url.QueryEscape(sid), http.StatusSeeOther)
}

// toReplace sends a client refused at the device limit to the choice, keeping their sign-in; token is the commerce cookie value when issued in this same response.
func (h *handler) toReplace(w http.ResponseWriter, r *http.Request, sess commerceSession, token string, asJSON bool) {
	if token != "" && h.commerceSessions != nil {
		h.commerceSessions.put(token, sess)
		setPortalCookie(w, r, commerceCookie, token, commerceSessionTTL)
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
    <button class="choice" type="submit" aria-label="{{.Label}}"{{if not $.CanSwap}} disabled{{end}}><span class="c-text"><span class="c-name">{{.Name}}</span><span class="c-detail">{{if .Online}}{{index $.T "dev.online"}}{{else}}{{index $.T "dev.offline"}}{{end}}{{if .LastSeen}} · <span class="dev-seen" data-at="{{.LastSeen}}"></span>{{end}}</span><span class="c-detail c-price">{{index $.T "rep.btn"}}</span></span>` + iconChevNext + `</button>
  </form>{{end}}
  </div>
  <form method="post" action="/logout"><input type="hidden" name="leave" value="{{.Leave}}"><button class="link" type="submit">{{index .T "pkg.signout"}}</button></form>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
<script nonce="{{.Nonce}}">
  // "Last used", in the device's own clock and language.
  (function () {
    var els = document.querySelectorAll('.dev-seen');
    for (var i = 0; i < els.length; i++) {
      var ts = Date.parse(els[i].getAttribute('data-at'));
      // In the PAGE's language (audit P-24), not whatever the browser defaults to.
      if (!isNaN(ts)) {
        var d = new Date(ts);
        try { els[i].textContent = d.toLocaleString(document.documentElement.lang || undefined); } catch (e) { els[i].textContent = d.toLocaleString(); }
      }
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
