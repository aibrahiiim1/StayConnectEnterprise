package ledger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPDest keeps the ledger on an SFTP server: a NAS, or a server the hotel or its IT provider runs.
//
// The server's identity is PINNED: the host key's SHA-256 fingerprint is confirmed by an administrator when the destination
// is set up, and a connection to a server presenting any other key is refused. (An unpinned destination can only be probed
// for its fingerprint.)
type SFTPDest struct {
	Host        string
	Port        int
	User        string
	Dir         string // absolute directory on the server
	Password    string
	PrivateKey  []byte // PEM, alternative to Password
	Fingerprint string // "SHA256:..." as ssh-keygen prints it
	Timeout     time.Duration
	// AllowLocal permits a server on this appliance (tests only; production refuses it, see Check).
	AllowLocal bool
}

func (d *SFTPDest) Kind() string { return "sftp" }
func (d *SFTPDest) Describe() string {
	return "sftp://" + d.User + "@" + net.JoinHostPort(d.Host, strconv.Itoa(d.port())) + d.Dir
}

func (d *SFTPDest) port() int {
	if d.Port == 0 {
		return 22
	}
	return d.Port
}

func (d *SFTPDest) timeout() time.Duration {
	if d.Timeout == 0 {
		return 30 * time.Second
	}
	return d.Timeout
}

func (d *SFTPDest) auth() ([]ssh.AuthMethod, error) {
	var m []ssh.AuthMethod
	if len(d.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(d.PrivateKey)
		if err != nil {
			return nil, errors.New("the private key could not be read")
		}
		m = append(m, ssh.PublicKeys(signer))
	}
	if d.Password != "" {
		m = append(m, ssh.Password(d.Password))
	}
	if len(m) == 0 {
		return nil, errors.New("a password or a private key is required")
	}
	return m, nil
}

var errStopProbe = errors.New("probe complete")

// ProbeHostKey connects without authenticating and returns the fingerprint the server presents, for the administrator to
// confirm. Nothing is sent but the handshake.
func (d *SFTPDest) ProbeHostKey(ctx context.Context) (string, error) {
	var fp string
	cfg := &ssh.ClientConfig{
		User: "probe", Timeout: d.timeout(),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp = ssh.FingerprintSHA256(key)
			return errStopProbe
		},
	}
	nd := net.Dialer{Timeout: d.timeout()}
	conn, err := nd.DialContext(ctx, "tcp", net.JoinHostPort(d.Host, strconv.Itoa(d.port())))
	if err != nil {
		return "", fmt.Errorf("the server cannot be reached: %w", err)
	}
	defer conn.Close()
	_, _, _, err = ssh.NewClientConn(conn, d.Host, cfg)
	if fp != "" {
		return fp, nil
	}
	return "", fmt.Errorf("the server did not present a host key: %v", err)
}

func (d *SFTPDest) connect(ctx context.Context) (*sftp.Client, func(), error) {
	if d.Fingerprint == "" {
		return nil, nil, errors.New("the server's host key has not been confirmed")
	}
	auth, err := d.auth()
	if err != nil {
		return nil, nil, err
	}
	want := d.Fingerprint
	cfg := &ssh.ClientConfig{
		User: d.User, Auth: auth, Timeout: d.timeout(),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != want {
				return fmt.Errorf("the server's host key changed (expected %s, got %s)", want, got)
			}
			return nil
		},
	}
	nd := net.Dialer{Timeout: d.timeout()}
	conn, err := nd.DialContext(ctx, "tcp", net.JoinHostPort(d.Host, strconv.Itoa(d.port())))
	if err != nil {
		return nil, nil, fmt.Errorf("the server cannot be reached: %w", err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, d.Host, cfg)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("the server refused the connection: %w", err)
	}
	cl, err := sftp.NewClient(ssh.NewClient(sc, chans, reqs))
	if err != nil {
		sc.Close()
		return nil, nil, fmt.Errorf("the server does not offer SFTP: %w", err)
	}
	return cl, func() { cl.Close(); sc.Close() }, nil
}

func (d *SFTPDest) Check(ctx context.Context) error {
	if !strings.HasPrefix(d.Dir, "/") {
		return errors.New("the folder on the server must be an absolute path")
	}
	if !d.AllowLocal && isLocalHost(d.Host) {
		return errors.New("that server is this appliance: a copy kept here is not independent of it")
	}
	return checkRoundTrip(ctx, d)
}

func (d *SFTPDest) objectPath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("ledger: bad object name %q", name)
	}
	return path.Join(d.Dir, name), nil
}

func (d *SFTPDest) Put(ctx context.Context, name string, data []byte) error {
	p, err := d.objectPath(name)
	if err != nil {
		return err
	}
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := cl.MkdirAll(d.Dir); err != nil {
		return fmt.Errorf("the folder on the server cannot be created: %w", err)
	}
	tmp := p + ".partial"
	f, err := cl.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = cl.Remove(tmp)
		return err
	}
	_ = f.Sync() // not every server supports fsync; the read-back by the caller is the proof
	if err := f.Close(); err != nil {
		_ = cl.Remove(tmp)
		return err
	}
	if err := cl.PosixRename(tmp, p); err != nil {
		// no posix-rename: replace in two steps (the read-back still proves the result)
		_ = cl.Remove(p)
		if err2 := cl.Rename(tmp, p); err2 != nil {
			_ = cl.Remove(tmp)
			return err2
		}
	}
	return nil
}

func (d *SFTPDest) Get(ctx context.Context, name string) ([]byte, error) {
	p, err := d.objectPath(name)
	if err != nil {
		return nil, err
	}
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	f, err := cl.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxObject))
}

// Remove deletes an object (used to clean the probe up).
func (d *SFTPDest) Remove(ctx context.Context, name string) error {
	p, err := d.objectPath(name)
	if err != nil {
		return err
	}
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	if err := cl.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// maxObject bounds what Get reads: a ledger is a few megabytes at the very most.
const maxObject = 512 << 20

func isLocalHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		for _, x := range ips {
			if !isLocalIP(x) {
				return false
			}
		}
		return true
	}
	return isLocalIP(ip)
}

func isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
