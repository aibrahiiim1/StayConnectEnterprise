package ledger

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	secret  string
	region  string
	bucket  string
	failPut bool
}

func newFakeS3(bucket, secret string) (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}, secret: secret, region: "eu-west-1", bucket: bucket}
	return f, httptest.NewServer(f)
}

func (f *fakeS3) deny(w http.ResponseWriter, code, status int) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>x</Message></Error>", code2(code))
}

func code2(c int) string {
	return map[int]string{0: "SignatureDoesNotMatch", 1: "NoSuchBucket", 2: "NoSuchKey", 3: "InternalError"}[c]
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")
	parts := strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ")
	if len(parts) != 3 {
		f.deny(w, 0, 403)
		return
	}
	cred := strings.TrimPrefix(parts[0], "Credential=")
	signed := strings.TrimPrefix(parts[1], "SignedHeaders=")
	sig := strings.TrimPrefix(parts[2], "Signature=")
	scope := strings.SplitN(cred, "/", 2)[1]
	date := r.Header.Get("x-amz-date")
	payload := sha256.Sum256(body)
	if r.Header.Get("x-amz-content-sha256") != hex.EncodeToString(payload[:]) {
		f.deny(w, 0, 400)
		return
	}
	var ch strings.Builder
	for _, h := range strings.Split(signed, ";") {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		ch.WriteString(h + ":" + v + "\n")
	}
	canon := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, ch.String(), signed, hex.EncodeToString(payload[:])}, "\n")
	cs := sha256.Sum256([]byte(canon))
	sts := "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(cs[:])
	mac := func(k []byte, s string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(s)); return h.Sum(nil) }
	sc := strings.Split(scope, "/")
	k := mac([]byte("AWS4"+f.secret), sc[0])
	k = mac(k, sc[1])
	k = mac(k, sc[2])
	k = mac(k, "aws4_request")
	if hex.EncodeToString(mac(k, sts)) != sig {
		f.deny(w, 0, 403)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != f.bucket {
		f.deny(w, 1, 404)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if f.failPut {
			f.deny(w, 3, 500)
			return
		}
		f.objects[key] = body
	case http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			f.deny(w, 2, 404)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(204)
	}
}

func TestS3Dest_SignedRequestsRoundTripAndWrongThingsAreRefused(t *testing.T) {
	fake, srv := newFakeS3("hotel-ledger", "s3cr3t")
	defer srv.Close()
	d := &S3Dest{Endpoint: srv.URL, Region: fake.region, Bucket: "hotel-ledger", Prefix: "site a/ledger", AccessKey: "AK", SecretKey: "s3cr3t", AllowLocal: true}
	ctx := context.Background()
	if err := d.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := d.Put(ctx, ExternalObject, []byte("hello ledger")); err != nil {
		t.Fatal(err)
	}
	b, err := d.Get(ctx, ExternalObject)
	if err != nil || string(b) != "hello ledger" {
		t.Fatalf("get = %q %v", b, err)
	}
	if _, ok := fake.objects["site a/ledger/"+ExternalObject]; !ok {
		t.Fatalf("object stored under %v", keys(fake.objects))
	}
	if _, err := d.Get(ctx, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	bad := *d
	bad.SecretKey = "wrong"
	if err := bad.Put(ctx, "x", []byte("y")); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("a wrong secret was accepted: %v", err)
	}
	nb := *d
	nb.Bucket = "other"
	if _, err := nb.Get(ctx, "x"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing bucket must not read as an empty ledger: %v", err)
	}
	fake.failPut = true
	if err := d.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("a failing write was reported as done")
	}
	// production refuses an appliance-local or plain-http endpoint
	prod := *d
	prod.AllowLocal = false
	if err := prod.Check(ctx); err == nil {
		t.Fatal("a plain-http local endpoint was accepted in production mode")
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}

func startSFTP(t *testing.T, root, password string) (host string, port int, fingerprint string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "ledger" && string(pw) == password {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "session" {
						_ = nc.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for rq := range creqs {
							ok := rq.Type == "subsystem" && len(rq.Payload) >= 4 && string(rq.Payload[4:]) == "sftp"
							_ = rq.Reply(ok, nil)
							if ok {
								srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
								if err == nil {
									_ = srv.Serve()
									srv.Close()
								}
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestSFTPDest_PinnedHostKeyRoundTripAndRefusals(t *testing.T) {
	root := t.TempDir()
	host, port, fp := startSFTP(t, root, "pw")
	ctx := context.Background()
	d := &SFTPDest{Host: host, Port: port, User: "ledger", Password: "pw", Dir: "store", AllowLocal: true, Timeout: 5 * time.Second}

	// unpinned: it can only be probed for its fingerprint
	if err := d.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("an unpinned server was used")
	}
	got, err := d.ProbeHostKey(ctx)
	if err != nil || got != fp {
		t.Fatalf("probe = %q %v, want %q", got, err, fp)
	}
	d.Fingerprint = fp
	if err := d.Put(ctx, ExternalObject, []byte("ledger bytes")); err != nil {
		t.Fatal(err)
	}
	b, err := d.Get(ctx, ExternalObject)
	if err != nil || string(b) != "ledger bytes" {
		t.Fatalf("get = %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "store", ExternalObject)); err != nil {
		t.Fatalf("not on the server's disk: %v", err)
	}
	if err := d.Put(ctx, ExternalObject, []byte("second")); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.Get(ctx, ExternalObject); string(b) != "second" {
		t.Fatalf("replace: %q", b)
	}
	if _, err := d.Get(ctx, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	wrong := *d
	wrong.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := wrong.Put(ctx, "x", []byte("y")); err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("a changed host key was accepted: %v", err)
	}
	badpw := *d
	badpw.Password = "nope"
	if err := badpw.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("a wrong password was accepted")
	}
	// production refuses a server on the appliance itself
	prod := *d
	prod.AllowLocal = false
	prod.Dir = "/store"
	if err := prod.Check(ctx); err == nil || !strings.Contains(err.Error(), "this appliance") {
		t.Fatalf("a local server was accepted: %v", err)
	}
	// a leftover partial file is not what Get returns
	es, _ := os.ReadDir(filepath.Join(root, "store"))
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}
