// Package nftconverge makes the live nftables ruleset match what the CURRENT binary renders, and does nothing
// at all when it already does.
//
// THE STORED BUNDLE IS NOT THE SOURCE OF TRUTH FOR RULESET STRUCTURE.
//
// netd used to re-assert the ruleset on boot by replaying `<bundle>/stayconnect.nft` — the file rendered at the
// time of the last network apply. That file is a snapshot of what an OLDER BINARY thought the ruleset should
// be, and nothing about it changes when the software changes. On the pilot appliance the active revision was
// rendered in July; every netd start replayed it, and because the render begins with `delete table inet
// stayconnect`, every start deleted `phase3_auth_ipv4` — a set the current software requires. The database was
// correct, the bundle was intact, and the structure was still wrong.
//
// Structure is a pure function of (intent, topology, RENDERER VERSION). Two of those live in the database; the
// third exists only in the running binary. So reconciliation renders from the current binary and compares
// against the kernel, and the stored bundle becomes what it always should have been: a record of what was
// applied, not the instruction for what to apply next.
//
// THE STEADY-STATE / UPGRADE DISTINCTION IS THE WHOLE SAFETY ARGUMENT.
//
//	steady state — the live fingerprint equals what this binary renders. Nothing is executed. Not a narrower
//	               nft command, not an idempotent re-apply: ZERO mutations. A routine restart or reboot of a
//	               correct appliance therefore cannot disturb guest authorization, because it does not touch
//	               the ruleset at all.
//	upgrade      — the live fingerprint differs (new software, a hand-edited table, or a fresh boot where only
//	               the static /etc/nftables.conf has loaded). The current render is applied as ONE atomic nft
//	               transaction, and the authorization that was live is carried across inside that same
//	               transaction, so converging does not deauthorize anyone either.
//
// Both halves matter. Skip-when-equal alone would still have made the one upgrade destructive; carry-over alone
// would have made every restart a rewrite of the live ruleset.
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

// Runner is the process boundary. It is an interface so that a test can observe exactly which commands a
// reconciliation issued — including, for the steady-state case, that it issued none. A test that could only
// inspect the resulting ruleset could not tell "already correct, did nothing" apart from "rewrote it to the
// same thing", and those differ by whether a live guest keeps their authorization.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Outcome is the result of one reconciliation. It is deliberately explicit about "changed nothing", because
// "changed nothing" is the property the production invariant depends on and the tests assert.
type Outcome struct {
	Changed   bool   `json:"changed"`
	Trigger   string `json:"trigger"`
	DesiredFP string `json:"desired_fp"`
	LiveFP    string `json:"live_fp"`
	Carried   int    `json:"carried_elements"`
	TableWas  bool   `json:"table_existed"`
}

// AuthSets are the packet-authorization sets whose contents must survive a converge. They are the only sets
// whose elements are runtime state rather than rendered structure.
var AuthSets = []string{nft.AuthV4, nft.Phase3AuthV4}

// tableName is the single StayConnect nft table. It is named once so that no read path can drift from the
// table the render actually replaces.
const tableName = "stayconnect"

// Engine reconciles one appliance's ruleset.
type Engine struct {
	Topo    netcfg.Topology
	Dir     string // where the applied render is written, for inspection
	NftPath string // "nft" unless a test points it at a namespace-scoped wrapper
	R       Runner
}

func (e *Engine) nftBin() string {
	if e.NftPath != "" {
		return e.NftPath
	}
	return "nft"
}

// Ensure makes the live ruleset match what this binary renders for the given intent.
func (e *Engine) Ensure(ctx context.Context, intent []netcfg.GuestNetwork, trigger string) (Outcome, error) {
	out := Outcome{Trigger: trigger, DesiredFP: netcfg.RenderFingerprint(intent, e.Topo)}

	live, err := e.ReadLive(ctx)
	if err != nil {
		// THE LIVE STATE COULD NOT BE ESTABLISHED. Converging from here would mean deciding what to delete
		// and what to re-authorize on the strength of a reading we know is unreliable, and the render begins
		// with `delete table`. Nothing is executed and the live ruleset is left exactly as it is.
		return out, fmt.Errorf("%w: %v", ErrLiveStateUntrusted, err)
	}
	out.LiveFP, out.TableWas = live.Fingerprint, live.TableExists

	if live.TableExists && live.Fingerprint == out.DesiredFP {
		// STEADY STATE. Return without executing anything.
		return out, nil
	}

	// UPGRADE / RECONSTRUCTION. Carry the live authorization across the atomic replace.
	var carried []string
	if live.TableExists {
		for _, set := range AuthSets {
			if !live.Sets[set] {
				// GENUINELY ABSENT, on the authority of the table listing itself — there is nothing to
				// carry. This is the normal case for phase3_auth_ipv4 on a pre-Phase-3 appliance.
				continue
			}
			els, err := e.readSet(ctx, set)
			if err != nil {
				// The set EXISTS and we could not read it. That is not the same as empty: converging
				// would silently deauthorize whoever is in it. Refuse before touching anything.
				return out, fmt.Errorf("%w: read live set %s: %v", ErrLiveStateUntrusted, set, err)
			}
			carried = append(carried, CarryOverCommands(set, els)...)
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

	// Verify against the kernel rather than trusting the exit code: a ruleset that loaded but did not produce
	// the expected structure is exactly the failure this whole mechanism exists to detect.
	if got, ok := e.LiveFingerprint(ctx); !ok || got != out.DesiredFP {
		return out, fmt.Errorf("nft applied but live fingerprint is %q, want %q", got, out.DesiredFP)
	}
	return out, nil
}

// ErrLiveStateUntrusted is returned when the live ruleset could not be established well enough to converge
// from. It is deliberately distinct from "the structure differs": one means act, the other means stop.
var ErrLiveStateUntrusted = errors.New("live nft state could not be read reliably")

// LiveState is what the kernel says it is holding right now.
type LiveState struct {
	TableExists bool
	Fingerprint string
	Sets        map[string]bool // names of the sets the table declares
}

// ReadLive establishes the live state, and returns an error rather than a guess whenever it cannot.
//
// ABSENCE IS DECIDED BY ENUMERATION, NEVER BY A FAILED READ. The obvious implementation asks nft for the set
// and treats a non-zero exit as "not there" — but `nft list set` exits non-zero for a missing set, a missing
// binary, a denied permission, a busy netlink socket and a malformed ruleset alike, and only the first of those
// means empty. Every other one would have been read as "no authorization to preserve" immediately before a
// `delete table`, which is precisely how a converge could deauthorize a whole property and report success.
//
// So the table listing — one command, which either works or fails as a whole — is the authority on which sets
// exist, and a set that IS listed must then be readable or the converge is abandoned.
func (e *Engine) ReadLive(ctx context.Context) (LiveState, error) {
	// 1. Is nft usable at all, and does our table exist? `list tables` answers both: if it fails we know
	//    nothing, and if it succeeds its enumeration is trustworthy.
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
		// GENUINELY ABSENT — a fresh boot, or an appliance that has never been configured. Not an error.
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
			// An unreadable marker comment yields "", which compares unequal to every fingerprint — a
			// marker we cannot parse must never be read as "already current".
			st.Fingerprint = netcfg.FingerprintFromSetComment(o.Set.Comment)
		}
	}
	return st, nil
}

// LiveFingerprint is the convenience form used by callers that only need the comparison. It reports no
// fingerprint and no table when the live state cannot be trusted, which is safe for those callers because they
// use it to decide whether to LOOK further, never to decide what to delete.
func (e *Engine) LiveFingerprint(ctx context.Context) (string, bool) {
	st, err := e.ReadLive(ctx)
	if err != nil {
		return "", false
	}
	return st.Fingerprint, st.TableExists
}

// readSet reads one authorization set that the table listing says EXISTS. Every failure is an error; there is
// no longer any path by which an unreadable set becomes an empty one.
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

// CarryOverCommands turns live elements into the `add element` lines that re-authorize them inside the same
// transaction as the structural replace.
//
// THE THREE CASES ARE NOT INTERCHANGEABLE, and collapsing any two of them is a security bug:
//
//	Expires > 0                  a live timed authorization. Carried with its REMAINING lease, never its
//	                             original: an element created with a 90s lease 89 seconds ago has one second
//	                             left, and re-adding it with 90s extends an authorization past the boundary
//	                             that granted it.
//	Timeout > 0, Expires <= 0    an EXPIRED authorization. The kernel was about to drop it; carrying it over
//	                             would resurrect it. Dropped.
//	Timeout == 0, Expires == 0   a PERMANENT element — which is what legacy scd writes into auth_ipv4. It has
//	                             no lease to preserve, so it is re-added with no timeout clause. Treating it
//	                             as expired would deauthorize every legacy guest on the appliance.
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
		}

		switch {
		case e.Expires > 0:
			secs := int(e.Expires / time.Second)
			if secs < 1 {
				continue // under a second left; the kernel will drop it either way
			}
			out = append(out, fmt.Sprintf("add element inet stayconnect %s { %s timeout %ds }", set, key, secs))
		case e.Timeout > 0:
			continue // expired
		default:
			out = append(out, fmt.Sprintf("add element inet stayconnect %s { %s }", set, key))
		}
	}
	return out
}
