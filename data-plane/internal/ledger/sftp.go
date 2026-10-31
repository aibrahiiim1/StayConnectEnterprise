package ledger

import (
	"context"
	"encoding/hex"
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

// SFTPDest keeps the ledger on an SFTP server. The host key's SHA-256 fingerprint is pinned by an administrator at
// setup and any other key is refused; an unpinned destination can only be probed for its fingerprint.
type SFTPDest struct {
	Host        string
	Port        int
	User        string
	Dir         string
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

// Check accepts an absolute folder or one relative to the account's login folder (a chrooted account often sees only that). The
// folder the objects really land in is what Locate reports; the round trip proves it can be written and read back.
func (d *SFTPDest) Check(ctx context.Context) error {
	if err := d.validDir(); err != nil {
		return err
	}
	if !d.AllowLocal && isLocalHost(d.Host) {
		return errors.New("that server is this appliance: a copy kept here is not independent of it")
	}
	return checkRoundTrip(ctx, d)
}

func (d *SFTPDest) validDir() error {
	dir := strings.TrimSpace(d.Dir)
	if dir == "" {
		return errors.New("the folder on the server is required (an absolute path, or one relative to the account's login folder)")
	}
	if strings.ContainsRune(dir, 0) || strings.Contains(dir, "\\") {
		return errors.New("the folder on the server must use / between folder names")
	}
	for _, part := range strings.Split(dir, "/") {
		if part == ".." {
			return errors.New("the folder on the server must not contain ..")
		}
	}
	return nil
}

func (d *SFTPDest) objectPath(name string) (string, error) {
	if err := validObjectName(name); err != nil {
		return "", err
	}
	return path.Join(d.Dir, name), nil
}

// prepare connects and makes sure the folder exists and is a folder.
func (d *SFTPDest) prepare(ctx context.Context) (*sftp.Client, func(), error) {
	if err := d.validDir(); err != nil {
		return nil, nil, err
	}
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := cl.MkdirAll(d.Dir); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("the folder on the server cannot be created (check the account's permissions on it): %w", err)
	}
	if fi, err := cl.Stat(d.Dir); err != nil || !fi.IsDir() {
		closeFn()
		return nil, nil, errors.New("the folder on the server is not a folder")
	}
	return cl, closeFn, nil
}

// writeAtomically writes through fill to a uniquely named temporary file beside the target and renames it into place. A failed
// write never leaves the temporary file behind, and never replaces the target with a partial one.
func (d *SFTPDest) writeAtomically(cl *sftp.Client, p string, fill func(io.Writer) error) error {
	tmp := p + "." + hex.EncodeToString(probeNonce()) + ".partial"
	f, err := cl.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("a file cannot be created in the folder on the server (check the account's permissions): %w", err)
	}
	if err := fill(f); err != nil {
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

func (d *SFTPDest) Put(ctx context.Context, name string, data []byte) error {
	p, err := d.objectPath(name)
	if err != nil {
		return err
	}
	cl, closeFn, err := d.prepare(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return d.writeAtomically(cl, p, func(w io.Writer) error { _, err := w.Write(data); return err })
}

func (d *SFTPDest) PutFile(ctx context.Context, name, localPath string) error {
	p, err := d.objectPath(name)
	if err != nil {
		return err
	}
	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	cl, closeFn, err := d.prepare(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	return d.writeAtomically(cl, p, func(w io.Writer) error { _, err := io.Copy(w, contextReader{ctx, in}); return err })
}

func (d *SFTPDest) Digest(ctx context.Context, name string) (string, int64, error) {
	p, err := d.objectPath(name)
	if err != nil {
		return "", 0, err
	}
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		return "", 0, err
	}
	defer closeFn()
	f, err := cl.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, ErrNotFound
		}
		return "", 0, err
	}
	defer f.Close()
	return digestReader(contextReader{ctx, f})
}

// Locate reports the folder as the server resolves it (for a chrooted account, inside its chroot) and, where the server offers
// the statvfs extension, the space left there.
func (d *SFTPDest) Locate(ctx context.Context) (Location, error) {
	loc := Location{Where: d.Dir, Medium: "sftp", Source: d.Describe()}
	cl, closeFn, err := d.prepare(ctx)
	if err != nil {
		return loc, err
	}
	defer closeFn()
	if rp, err := cl.RealPath(d.Dir); err == nil && rp != "" {
		loc.Where = rp
	}
	if vfs, err := cl.StatVFS(d.Dir); err == nil && vfs != nil {
		loc.FreeBytes, loc.TotalBytes = int64(vfs.Bavail*vfs.Frsize), int64(vfs.Blocks*vfs.Frsize)
	}
	return loc, nil
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
