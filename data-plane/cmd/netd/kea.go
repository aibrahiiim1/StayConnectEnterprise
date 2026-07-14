package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// keaClient speaks the Kea control-socket protocol (a length-unframed JSON
// request/response over a unix stream socket). We use config-test to validate
// a candidate config without applying it, config-set to apply it live, and
// config-write to persist it to the on-disk kea-dhcp4.conf so it survives a
// cold restart. No Kea restart and no CSV parsing anywhere.
type keaClient struct {
	socket string
}

func newKeaClient(socket string) *keaClient { return &keaClient{socket: socket} }

func (k *keaClient) cmd(command string, args any) (map[string]any, error) {
	c, err := net.DialTimeout("unix", k.socket, 5*time.Second)
	if err != nil {
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

// ConfigTest validates a candidate Dhcp4 config without applying it.
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

// ConfigSet applies a candidate Dhcp4 config live, then persists it to disk.
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

// Status returns true when the dhcp4 service answers status-get.
func (k *keaClient) Healthy() bool {
	resp, err := k.cmd("status-get", nil)
	if err != nil {
		return false
	}
	code, _ := resultOK(resp)
	return code == 0
}

// KeaLease is a lease row as returned by lease4-get-all.
type KeaLease struct {
	IPAddress string `json:"ip-address"`
	HWAddr    string `json:"hw-address"`
	Hostname  string `json:"hostname"`
	SubnetID  int    `json:"subnet-id"`
	State     int    `json:"state"`
	CLTT      int64  `json:"cltt"`
	ValidLft  int    `json:"valid-lft"`
}

// Leases returns all current DHCPv4 leases from the memfile backend via the
// control socket (structured — never parses the CSV).
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
