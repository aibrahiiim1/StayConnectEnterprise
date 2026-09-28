package postinghandoff

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

const body = "PS|RN1421|G#5|TA1500|PT|SOOG|CTWIFI|P#42|WSOG|"

func req() Request {
	return Request{Version: ProtocolVersion, InterfaceID: "i", PNumber: 42, Body: body, BodySHA256: BodyHash(body), WaitMillis: 1000}
}

func TestValidateAndPNumber(t *testing.T) {
	if err := req().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Request){
		"version":      func(r *Request) { r.Version = 2 },
		"hash":         func(r *Request) { r.BodySHA256 = BodyHash("x") },
		"p#":           func(r *Request) { r.PNumber = 43 },
		"not PS":       func(r *Request) { r.Body = "PA|P#42|ASOK|"; r.BodySHA256 = BodyHash(r.Body) },
		"control byte": func(r *Request) { r.Body = "PS|RN1\x03|P#42|"; r.BodySHA256 = BodyHash(r.Body) },
		"two P#":       func(r *Request) { r.Body = "PS|P#42|P#42|"; r.BodySHA256 = BodyHash(r.Body) },
	} {
		r := req()
		mut(&r)
		if r.Validate() == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	if PNumberOf("PA|RN1|P#77|ASOK|") != 77 || PNumberOf("PA|P#7a|") != 0 || PNumberOf("PA|ASOK|") != 0 {
		t.Fatal("P# extraction")
	}
}

func TestSendOverAUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		var got Request
		_ = json.Unmarshal(line, &got)
		resp := Response{Result: NotTransmitted, Code: "BAD"}
		if got.Body == body && got.Validate() == nil {
			resp = Response{Result: Answered, PABody: "PA|P#42|ASOK|"}
		}
		b, _ := json.Marshal(resp)
		_, _ = c.Write(append(b, '\n'))
	}()
	resp, err := Send(context.Background(), path, req())
	if err != nil || resp.Result != Answered || resp.PABody != "PA|P#42|ASOK|" {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := Send(context.Background(), filepath.Join(t.TempDir(), "absent.sock"), req()); err != ErrNotSent {
		t.Fatalf("an unreachable pmsd is provably not-sent, got %v", err)
	}
}
