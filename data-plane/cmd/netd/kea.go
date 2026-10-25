package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"
	"time"
)

// keaClient speaks the Kea control-socket protocol (unframed JSON over a unix stream socket): config-test to
// validate and config-set to apply live. Persistence is netd's own atomic write of the confirmed configuration, not config-write.
// No Kea restart and no CSV parsing.
type keaClient struct {
	socket string

	// A short-lived view of Kea's age and subnets for EmptyAnswerTrusted: with an empty table every session asks the same question in one pass.
	evMu      sync.Mutex
	evAt      time.Time
	evUptime  float64
	evReload  float64
	evSubnets []*net.IPNet
	evErr     error
}

func newKeaClient(socket string) *keaClient { return &keaClient{socket: socket} }

func (k *keaClient) cmd(command string, args any) (map[string]any, error) {
	c, err := net.DialTimeout("unix", k.socket, 5*time.Second)
	if err != nil {
		// An absent socket means DHCP is not running yet, not a fault: Kea binds the LAN bridge and cannot start
		// on a factory-clean appliance until guest networking is configured.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("DHCP is not running yet: Kea has no control socket at %s. "+
				"Kea serves the LAN bridge, so it starts once guest networking is configured on this "+
				"appliance", k.socket)
		}
		return nil, fmt.Errorf("kea dial: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))

	req := map[string]any{"command": command, "service": []string{"dhcp4"}}
	if args != nil {
		req["arguments"] = args
	}
	raw, _ := json.Marshal(req)
	if _, err := c.Write(raw); err != nil {
		return nil, err
	}
	// Kea reads until EOF on the write half.
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	body, err := io.ReadAll(c)
	if err != nil {
		return nil, err
	}
	// Response is either an object or a single-element array (per service).
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return arr[0], nil
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("kea response parse: %w (%.120s)", err, string(body))
	}
	return obj, nil
}

func resultOK(resp map[string]any) (int, string) {
	code := 1
	if v, ok := resp["result"].(float64); ok {
		code = int(v)
	}
	text, _ := resp["text"].(string)
	return code, text
}

func (k *keaClient) ConfigTest(dhcp4 map[string]any) error {
	resp, err := k.cmd("config-test", map[string]any{"Dhcp4": dhcp4})
	if err != nil {
		return err
	}
	if code, text := resultOK(resp); code != 0 {
		return fmt.Errorf("kea config-test failed: %s", text)
	}
	return nil
}

func (k *keaClient) ConfigSet(dhcp4 map[string]any) error {
	resp, err := k.cmd("config-set", map[string]any{"Dhcp4": dhcp4})
	if err != nil {
		return err
	}
	if code, text := resultOK(resp); code != 0 {
		return fmt.Errorf("kea config-set failed: %s", text)
	}
	// Deliberately NOT persisted here (no config-write): this is the LIVE configuration, which can be one that is still awaiting
	// confirmation or is about to be rolled back. The file a cold Kea starts from is written by netd from the confirmed
	// configuration only (kea_persist.go), atomically and with its failures reported.
	return nil
}

// RunningDhcp4 returns the configuration Kea is running (config-get); the file on disk and netd's last push can differ.
func (k *keaClient) RunningDhcp4() (map[string]any, error) {
	resp, err := k.cmd("config-get", nil)
	if err != nil {
		return nil, err
	}
	if code, text := resultOK(resp); code != 0 {
		return nil, fmt.Errorf("kea config-get failed: %s", text)
	}
	args, _ := resp["arguments"].(map[string]any)
	dhcp4, ok := args["Dhcp4"].(map[string]any)
	if !ok {
		return nil, errors.New("kea config-get returned no Dhcp4 object")
	}
	return dhcp4, nil
}

func (k *keaClient) Healthy() bool {
	resp, err := k.cmd("status-get", nil)
	if err != nil {
		return false
	}
	code, _ := resultOK(resp)
	return code == 0
}

type KeaLease struct {
	IPAddress string `json:"ip-address"`
	HWAddr    string `json:"hw-address"`
	Hostname  string `json:"hostname"`
	SubnetID  int    `json:"subnet-id"`
	State     int    `json:"state"`
	CLTT      int64  `json:"cltt"`
	ValidLft  int    `json:"valid-lft"`
}

// LeaseFile reports the memfile path from Kea's running configuration. When memfile cannot open its database
// Kea still answers status-get but refuses lease commands, with the reason on disk next to the lease file.
func (k *keaClient) LeaseFile() (string, error) {
	resp, err := k.cmd("config-get", nil)
	if err != nil {
		return "", err
	}
	if code, text := resultOK(resp); code != 0 {
		return "", fmt.Errorf("config-get: %s", text)
	}
	args, _ := resp["arguments"].(map[string]any)
	dhcp4, _ := args["Dhcp4"].(map[string]any)
	db, _ := dhcp4["lease-database"].(map[string]any)
	name, _ := db["name"].(string)
	if strings.TrimSpace(name) == "" {
		// A non-memfile backend (or none named) has no such file, and that is not a fault.
		return "", nil
	}
	return name, nil
}

// keaEvidenceSettle is how long Kea must have been running, and unreconfigured, before an empty lease table is believed. Long enough for clients
// to renew into a restarted or re-pointed lease file (their renewal timers are a fraction of the lease time), short enough that a genuinely
// idle network is recognised. Access never extends over it: an unverified address simply is not renewed (see addressOwner).
const keaEvidenceSettle = 5 * time.Minute

// AbsenceTrusted reports whether Kea having no lease for ip can be believed: it has run and stayed configured for keaEvidenceSettle, and
// its running configuration has a subnet that contains ip (an address Kea does not serve has no lease whatever the table says).
func (k *keaClient) AbsenceTrusted(ip net.IP) (bool, string) {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	if time.Since(k.evAt) > 3*time.Second {
		k.evUptime, k.evReload, k.evSubnets, k.evErr = k.readStability()
		k.evAt = time.Now()
	}
	if k.evErr != nil {
		return false, k.evErr.Error()
	}
	settle := keaEvidenceSettle.Seconds()
	if k.evUptime < settle {
		return false, fmt.Sprintf("Kea started %.0f s ago (less than %.0f s)", k.evUptime, settle)
	}
	if k.evReload < settle {
		return false, fmt.Sprintf("Kea was reconfigured %.0f s ago (less than %.0f s)", k.evReload, settle)
	}
	for _, n := range k.evSubnets {
		if n.Contains(ip) {
			return true, ""
		}
	}
	return false, "Kea's running configuration has no subnet that contains " + ip.String()
}

// Counters returns Kea's server-wide DHCP packet counters. Per-subnet counters are deliberately not read.
func (k *keaClient) Counters() (map[string]int, error) {
	resp, err := k.cmd("statistic-get-all", nil)
	if err != nil {
		return nil, err
	}
	if code, text := resultOK(resp); code != 0 {
		return nil, fmt.Errorf("statistic-get-all: %s", text)
	}
	args, _ := resp["arguments"].(map[string]any)
	out := map[string]int{}
	for _, name := range []string{"pkt4-received", "pkt4-discover-received", "pkt4-offer-sent", "pkt4-request-received", "pkt4-ack-sent", "pkt4-nak-sent"} {
		series, _ := args[name].([]any)
		if len(series) == 0 {
			continue
		}
		point, _ := series[0].([]any)
		if len(point) == 0 {
			continue
		}
		if v, ok := point[0].(float64); ok {
			out[name] = int(v)
		}
	}
	return out, nil
}

// KeaEpoch is when the running Kea instance started, derived from its reported uptime.
func (k *keaClient) KeaEpoch() (time.Time, bool) {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	if time.Since(k.evAt) > 3*time.Second {
		k.evUptime, k.evReload, k.evSubnets, k.evErr = k.readStability()
		k.evAt = time.Now()
	}
	if k.evErr != nil {
		return time.Time{}, false
	}
	return k.evAt.Add(-time.Duration(k.evUptime * float64(time.Second))).Truncate(time.Second), true
}

// readStability asks Kea how long it has run and been configured, and which subnets it serves.
func (k *keaClient) readStability() (uptime, reload float64, subnets []*net.IPNet, err error) {
	resp, err := k.cmd("status-get", nil)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("Kea status could not be read: %w", err)
	}
	if code, text := resultOK(resp); code != 0 {
		return 0, 0, nil, fmt.Errorf("Kea status-get failed: %s", text)
	}
	args, _ := resp["arguments"].(map[string]any)
	uptime, okU := args["uptime"].(float64)
	reload, okR := args["reload"].(float64)
	if !okU || !okR {
		return 0, 0, nil, errors.New("Kea did not report how long it has been running, so its stability cannot be judged")
	}
	dhcp4, err := k.RunningDhcp4()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("Kea's running configuration could not be read: %w", err)
	}
	list, _ := dhcp4["subnet4"].([]any)
	for _, s := range list {
		m, _ := s.(map[string]any)
		cidr, _ := m["subnet"].(string)
		if _, n, perr := net.ParseCIDR(cidr); perr == nil {
			subnets = append(subnets, n)
		}
	}
	return uptime, reload, subnets, nil
}

// Leases returns all current DHCPv4 leases via the control socket (never parses the CSV).
func (k *keaClient) Leases() ([]KeaLease, error) {
	resp, err := k.cmd("lease4-get-all", nil)
	if err != nil {
		return nil, err
	}
	code, text := resultOK(resp)
	// result 3 = empty (no leases) is not an error.
	if code == 3 {
		return nil, nil
	}
	if code != 0 {
		return nil, fmt.Errorf("lease4-get-all: %s", text)
	}
	argsRaw, _ := json.Marshal(resp["arguments"])
	var out struct {
		Leases []KeaLease `json:"leases"`
	}
	_ = json.Unmarshal(argsRaw, &out)
	return out.Leases, nil
}
