package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"time"
)

// keaClient speaks the Kea control-socket protocol (unframed JSON over a unix stream socket): config-test to
// validate, config-set to apply live, config-write to persist. No Kea restart and no CSV parsing.
type keaClient struct {
	socket string
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
	// Persist so a cold Kea start uses the same config.
	if _, err := k.cmd("config-write", nil); err != nil {
		return fmt.Errorf("kea config-write: %w", err)
	}
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
