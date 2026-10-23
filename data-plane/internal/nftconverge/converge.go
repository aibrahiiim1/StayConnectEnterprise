// Package nftconverge makes the live nftables ruleset match what the current binary renders, and does nothing when it
// already does.
//
// The stored bundle is not the source of truth for structure: it is a snapshot of what an older binary rendered, and
// replaying it (its `delete table` removed phase3_auth_ipv4, which current software requires) breaks the ruleset.
// Structure is a function of (intent, topology, renderer version), so reconciliation renders from the current binary
// and compares against the kernel.
//
// Steady state (live fingerprint equals the render): nothing is executed, so a routine restart cannot disturb guest
// authorization. Upgrade (fingerprint differs: new software, a hand-edited table, or a boot with only
// /etc/nftables.conf): the current render is applied as one atomic nft transaction with the live authorization
// carried across inside it. Both halves matter: skip-when-equal alone leaves the upgrade destructive, and carry-over
// alone makes every restart a rewrite.
package nftconverge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// Runner is the process boundary, an interface so tests can observe exactly which commands a reconciliation issued,
// including that the steady state issues none.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Outcome is the result of one reconciliation, explicit about "changed nothing".
type Outcome struct {
	Changed   bool   `json:"changed"`
	Trigger   string `json:"trigger"`
	DesiredFP string `json:"desired_fp"`
	LiveFP    string `json:"live_fp"`
	Carried   int    `json:"carried_elements"`
	TableWas  bool   `json:"table_existed"`
}

// AuthSets are the sets whose contents must survive a converge: the two packet-authorization sets and the walled
// garden (runtime state, not rendered structure). Only sets the current render declares may be listed: a carried
// `add element` into a set the new table lacks fails the whole `nft -f`. An older table still holding the retired
// verification-access set converges by the ordinary `delete table` + recreate, which removes it and its maps and
// objects without reading or carrying them.
var AuthSets = []string{nft.AuthV4, nft.Phase3AuthV4, nft.GardenV4, nft.GardenTCP, nft.GardenUDP}

// tableName is the single StayConnect nft table, named once so no read path drifts from the table the render replaces.
const tableName = "stayconnect"

type Engine struct {
	Topo    netcfg.Topology
	Dir     string
	NftPath string // "nft" unless a test points it at a namespace-scoped wrapper
	R       Runner
}

func (e *Engine) nftBin() string {
	if e.NftPath != "" {
		return e.NftPath
	}
	return "nft"
}

func (e *Engine) Ensure(ctx context.Context, intent []netcfg.GuestNetwork, trigger string) (Outcome, error) {
	out := Outcome{Trigger: trigger, DesiredFP: netcfg.RenderFingerprint(intent, e.Topo)}

	live, err := e.ReadLive(ctx)
	if err != nil {
		// The live state could not be established: converging would mean deciding what to delete on an unreliable reading,
		// and the render begins with `delete table`. Nothing is executed.
		return out, fmt.Errorf("%w: %v", ErrLiveStateUntrusted, err)
	}
	out.LiveFP, out.TableWas = live.Fingerprint, live.TableExists

	if live.TableExists && live.Fingerprint == out.DesiredFP {
		// Steady state: execute nothing.
		return out, nil
	}

	// Upgrade or reconstruction: carry the live authorization across the atomic replace.
	var carried []string
	if live.TableExists {
		for _, set := range AuthSets {
			if !live.Sets[set] {
				// Genuinely absent per the table listing: nothing to carry (normal for phase3_auth_ipv4 on a pre-Phase-3 appliance).
				continue
			}
			els, err := e.readSet(ctx, set)
			if err != nil {
				// The set exists but could not be read, which is not the same as empty: converging would silently deauthorize
				// whoever is in it. Refuse before touching anything.
				return out, fmt.Errorf("%w: read live set %s: %v", ErrLiveStateUntrusted, set, err)
			}
			carried = append(carried, CarryOverCommands(set, withoutRendered(set, els))...)
		}
	}
	out.Carried = len(carried)

	script := string(netcfg.RenderNftables(intent, e.Topo))
	if len(carried) > 0 {
		script += "\n# authorization carried across the structural replace, in the SAME nft transaction\n" +
			strings.Join(carried, "\n") + "\n"
	}

	dir := e.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return out, err
	}
	path := filepath.Join(dir, "current-stayconnect.nft")
	if err := os.WriteFile(path, []byte(script), 0o640); err != nil {
		return out, err
	}
	if err := e.R.Run(ctx, e.nftBin(), "-f", path); err != nil {
		return out, err
	}
	out.Changed = true

	// Verify against the kernel rather than the exit code: a ruleset that loaded but lacks the expected structure is
	// the failure this mechanism exists to detect.
	if got, ok := e.LiveFingerprint(ctx); !ok || got != out.DesiredFP {
		return out, fmt.Errorf("nft applied but live fingerprint is %q, want %q", got, out.DesiredFP)
	}
	return out, nil
}

// ErrLiveStateUntrusted is returned when the live ruleset could not be established well enough to converge from;
// distinct from "the structure differs" (one means act, the other stop).
var ErrLiveStateUntrusted = errors.New("live nft state could not be read reliably")

type LiveState struct {
	TableExists bool
	Fingerprint string
	Sets        map[string]bool // names of the sets the table declares
}

// ReadLive establishes the live state, returning an error rather than a guess. Absence is decided by enumeration,
// never by a failed read: `nft list set` exits non-zero for a missing set, missing binary, denied permission, busy
// netlink and malformed ruleset alike, and only the first means empty. The table listing (one command that works or
// fails as a whole) is the authority on which sets exist, and a listed set must then be readable or the converge is
// abandoned.
func (e *Engine) ReadLive(ctx context.Context) (LiveState, error) {
	// 1. Is nft usable and does our table exist? `list tables` answers both; if it fails we know nothing.
	raw, err := e.R.Output(ctx, e.nftBin(), "-j", "list", "tables", "inet")
	if err != nil {
		return LiveState{}, fmt.Errorf("list tables: %w", err)
	}
	var tdoc struct {
		Nftables []struct {
			Table *struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &tdoc); err != nil {
		return LiveState{}, fmt.Errorf("list tables: unparseable output: %w", err)
	}
	present := false
	for _, o := range tdoc.Nftables {
		if o.Table != nil && o.Table.Name == tableName && (o.Table.Family == "" || o.Table.Family == "inet") {
			present = true
			break
		}
	}
	if !present {
		// Genuinely absent: a fresh boot or an unconfigured appliance. Not an error.
		return LiveState{TableExists: false, Sets: map[string]bool{}}, nil
	}

	// 2. The table exists, so it must be readable.
	raw, err = e.R.Output(ctx, e.nftBin(), "-j", "list", "table", "inet", tableName)
	if err != nil {
		return LiveState{}, fmt.Errorf("list table %s: %w", tableName, err)
	}
	var doc struct {
		Nftables []struct {
			Set *struct {
				Name    string `json:"name"`
				Comment string `json:"comment"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return LiveState{}, fmt.Errorf("list table %s: unparseable output: %w", tableName, err)
	}
	st := LiveState{TableExists: true, Sets: map[string]bool{}}
	for _, o := range doc.Nftables {
		if o.Set == nil {
			continue
		}
		st.Sets[o.Set.Name] = true
		if o.Set.Name == netcfg.RenderMarkerSet {
			// An unreadable marker yields "", which compares unequal to every fingerprint, so it is never read as already current.
			st.Fingerprint = netcfg.FingerprintFromSetComment(o.Set.Comment)
		}
	}
	return st, nil
}

// LiveFingerprint is the convenience form for callers that only compare. It reports no fingerprint and no table when
// the live state cannot be trusted, which is safe because they use it to decide whether to look further, never what
// to delete.
func (e *Engine) LiveFingerprint(ctx context.Context) (string, bool) {
	st, err := e.ReadLive(ctx)
	if err != nil {
		return "", false
	}
	return st.Fingerprint, st.TableExists
}

// readSet reads one authorization set the table listing says exists; every failure is an error, never an empty set.
func (e *Engine) readSet(ctx context.Context, set string) ([]nft.Element, error) {
	raw, err := e.R.Output(ctx, e.nftBin(), "-j", "list", "set", "inet", tableName, set)
	if err != nil {
		return nil, err
	}
	els, err := nft.ParseSetJSON(raw)
	if err != nil {
		return nil, err
	}
	return els, nil
}

// CarryOverCommands turns live elements into `add element` lines that re-authorize them inside the same transaction
// as the structural replace. The three cases are not interchangeable:
//   - Expires > 0: a live timed authorization, carried with its remaining lease, never the original (re-adding with
//     90s would extend it past the boundary that granted it).
//   - Timeout > 0, Expires <= 0: expired; carrying it over would resurrect it, so it is dropped.
//   - Timeout == 0, Expires == 0: permanent (what legacy scd writes into auth_ipv4), re-added with no timeout clause;
//     treating it as expired would deauthorize every legacy guest.
func CarryOverCommands(set string, els []nft.Element) []string {
	var out []string
	for _, e := range els {
		ip := e.IP
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if ip == nil || ip.Equal(net.IPv4zero) {
			continue
		}
		key := ip.String()
		if e.Iface != "" {
			key = fmt.Sprintf("%q . %s", e.Iface, ip.String())
		} else if e.Port > 0 {
			// A port-restricted garden element (`address . service`), carried as the pair it is.
			key = fmt.Sprintf("%s . %d", ip.String(), e.Port)
		}

		switch {
		case e.Expires > 0:
			secs := int(e.Expires / time.Second)
			if secs < 1 {
				continue // under a second left; the kernel will drop it either way
			}
			out = append(out, fmt.Sprintf("add element inet stayconnect %s { %s timeout %ds }", set, key, secs))
		case e.Timeout > 0:
			continue
		default:
			out = append(out, fmt.Sprintf("add element inet stayconnect %s { %s }", set, key))
		}
	}
	return out
}

// withoutRendered drops the elements the new render declares itself (the baseline resolver pairs of the
// port-restricted garden sets) so the converge carries and reports only runtime state.
func withoutRendered(set string, els []nft.Element) []nft.Element {
	if set != nft.GardenTCP && set != nft.GardenUDP {
		return els
	}
	static := map[string]bool{}
	for _, ip := range netcfg.BaselineResolvers {
		static[ip] = true
	}
	out := make([]nft.Element, 0, len(els))
	for _, e := range els {
		ip := e.IP
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if e.Port == netcfg.BaselineResolverPort && static[ip.String()] {
			continue
		}
		out = append(out, e)
	}
	return out
}
