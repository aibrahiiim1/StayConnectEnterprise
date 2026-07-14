package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/data-plane/internal/updates"
)

// updatesRoot is where staged/installed update components live. Each component
// has releases/<version> dirs and a `current` symlink flipped atomically.
const updatesRoot = "/opt/stayconnect/updates"
const applianceModel = "sc-appliance"

// startUpdateAgent subscribes to signed update assignments and installs them
// atomically with a built-in health check and automatic rollback. Packages are
// NEVER built here; signature + checksum + compatibility are verified first.
func (s *server) startUpdateAgent(ctx context.Context, nc *nats.Conn, applianceID, pubKeyPath string) error {
	raw, err := os.ReadFile(pubKeyPath)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		slog.Warn("updates: update-signing pubkey unavailable — update agent disabled", "path", pubKeyPath)
		return nil
	}
	pub := ed25519.PublicKey(raw)
	if s.db != nil {
		_, _ = s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS edge_installed_updates (
            update_id UUID PRIMARY KEY, component TEXT, version TEXT, status TEXT,
            installed_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	}
	sub, err := nc.Subscribe("appliances."+applianceID+".updates", func(m *nats.Msg) {
		s.handleUpdate(ctx, nc, pub, applianceID, m.Data)
	})
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = sub.Drain() }()
	slog.Info("updates: agent subscribed", "subject", "appliances."+applianceID+".updates")
	return nil
}

func (s *server) handleUpdate(ctx context.Context, nc *nats.Conn, pub ed25519.PublicKey, applianceID string, data []byte) {
	var msg struct {
		Manifest   updates.Manifest `json:"manifest"`
		PackageB64 string           `json:"package_b64"`
	}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	report := func(status string, res map[string]any) {
		body, _ := json.Marshal(map[string]any{"update_id": msg.Manifest.UpdateID, "status": status, "result": res})
		_ = nc.Publish("appliances."+applianceID+".updates.status", body)
		_ = nc.Flush()
	}
	pkg, err := base64.StdEncoding.DecodeString(msg.PackageB64)
	if err != nil {
		report("rejected", map[string]any{"reason": "package decode failed"})
		return
	}
	// Verify signature + checksum + component/model/version compatibility.
	if reason := updates.Accept(pub, &msg.Manifest, pkg, scdVersion, applianceModel); reason != "" {
		report("rejected", map[string]any{"reason": reason})
		return
	}
	// Duplicate: already installed → do not reinstall.
	if s.db != nil {
		var n int
		s.db.QueryRow(ctx, `SELECT count(*) FROM edge_installed_updates WHERE update_id=$1`, msg.Manifest.UpdateID).Scan(&n)
		if n > 0 {
			report("succeeded", map[string]any{"note": "already installed (idempotent)"})
			return
		}
	}
	status, res := s.installUpdate(&msg.Manifest, pkg)
	if s.db != nil && status == "succeeded" {
		_, _ = s.db.Exec(ctx, `INSERT INTO edge_installed_updates (update_id, component, version, status)
            VALUES ($1,$2,$3,$4) ON CONFLICT (update_id) DO NOTHING`, msg.Manifest.UpdateID, msg.Manifest.Component, msg.Manifest.Version, status)
	}
	report(status, res)
}

// installUpdate stages the package, atomically switches the `current` symlink,
// runs a BUILT-IN health check (no arbitrary shell), and rolls back on failure.
func (s *server) installUpdate(m *updates.Manifest, pkg []byte) (string, map[string]any) {
	if strings.ContainsAny(m.Component, "/.") { // never a path
		return "rejected", map[string]any{"reason": "invalid component name"}
	}
	compDir := filepath.Join(updatesRoot, m.Component)
	relDir := filepath.Join(compDir, "releases", m.Version)
	curLink := filepath.Join(compDir, "current")
	if err := os.MkdirAll(filepath.Join(compDir, "releases"), 0o755); err != nil {
		return "failed", map[string]any{"reason": err.Error()}
	}
	// Preserve current (for rollback).
	prevTarget, _ := os.Readlink(curLink)
	// Extract into staging.
	if err := extractTarGz(pkg, relDir); err != nil {
		return "failed", map[string]any{"reason": "extract: " + err.Error()}
	}
	// Atomic switch.
	tmp := curLink + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(relDir, tmp); err != nil {
		return "failed", map[string]any{"reason": "symlink: " + err.Error()}
	}
	if err := os.Rename(tmp, curLink); err != nil {
		return "failed", map[string]any{"reason": "switch: " + err.Error()}
	}
	// Built-in health check: current → new release AND VERSION file == manifest version.
	if healthy := s.updateHealthy(curLink, m.Version); !healthy {
		// Roll back to the previous release.
		if prevTarget != "" {
			_ = os.Remove(tmp)
			_ = os.Symlink(prevTarget, tmp)
			_ = os.Rename(tmp, curLink)
		}
		return "failed", map[string]any{"reason": "health check failed — rolled back", "rolled_back_to": prevTarget}
	}
	return "succeeded", map[string]any{"version": m.Version, "component": m.Component}
}

func (s *server) updateHealthy(curLink, wantVersion string) bool {
	target, err := os.Readlink(curLink)
	if err != nil {
		return false
	}
	v, err := os.ReadFile(filepath.Join(target, "VERSION"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(v)) == wantVersion
}

func extractTarGz(data []byte, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Path-traversal guard: never write outside dest.
		name := filepath.Clean(h.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

var _ = time.Now
