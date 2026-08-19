package main

// Resolving the voucher blind-index key from its GENERATION.
//
// A voucher pins code_key_generation_id, and that generation row carries the per-generation HMAC key sealed
// as hmac_key_ciphertext under encryption_key_id. Authentication therefore cannot know which key indexed a
// submitted code until it has looked -- so it computes the index under every generation that is still usable
// and lets the repository match.
//
// That "try each generation" shape is what makes rotation safe: after a new generation supersedes the old
// one, vouchers issued under the old key keep authenticating until they are redeemed or expire, exactly as
// the accepted design requires ("old-unsuperseded/unredeemed voucher usability"). A single-key scheme cannot
// express that, which is why the raw-file approach this replaces was wrong.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// generationKeyCache holds the opened blind-index keys for one (tenant, site).
//
// KEYED BY OWNER, not global. An earlier version used a single package-level cache with no tenant or site in
// the key: on a multi-site appliance the first site to authenticate would populate it and every other site
// would then index submitted codes under the WRONG site's key. Every lookup would miss, so vouchers would
// appear invalid rather than leak across sites -- a fail-closed bug, but a total outage for every site but
// one, and invisible on a single-site appliance like this DEVELOPMENT one.
//
// Keys are held in memory only, never logged, and vanish with the process.
type generationKeyCache struct {
	mu sync.RWMutex
	m  map[string]cachedGenerationKeys
}

type cachedGenerationKeys struct {
	loadedAt time.Time
	keys     [][]byte // clear per-generation HMAC keys, newest generation first
}

var vkCache = generationKeyCache{m: map[string]cachedGenerationKeys{}}

func vkKey(tenantID, siteID string) string { return tenantID + "|" + siteID }

// invalidateVoucherKeyCache drops this owner's cached generation keys.
//
// Minting a generation makes the cache WRONG, not merely stale: a code issued under the new key indexes to
// something none of the cached keys can produce, so it authenticates as "invalid_code" until the TTL expires.
// That was observable -- the rotation proof needed a 60-second sleep between issuing under a new generation
// and redeeming, which is not a property any real deployment should have to live with.
//
// Issuance runs in this process, so it invalidates directly. The TTL remains as the backstop for a
// generation minted by some other process or by an operator working in SQL: that case still self-heals
// within the TTL rather than requiring a restart.
func invalidateVoucherKeyCache(tenantID, siteID string) {
	vkCache.mu.Lock()
	delete(vkCache.m, vkKey(tenantID, siteID))
	vkCache.mu.Unlock()
}

// newGenerationVoucherHMAC returns the candidate-index function the authenticator calls.
//
// It returns ONE INDEX PER USABLE GENERATION, newest first, including superseded ones. That is what makes a
// rotation safe: a voucher issued under generation 1 keeps its generation-1 index forever, so after a
// rotation to generation 2 the only way it can still be found is for the lookup to try generation 1's key
// too. Returning just the active key -- which an earlier version did -- silently invalidates every
// unredeemed voucher the moment a new generation is created.
func newGenerationVoucherHMAC(pool *pgxpool.Pool, kr iamv2.VoucherKeyring) iamv2.VoucherHMACCandidates {
	return func(ctx context.Context, tenantID, siteID, code string) ([][]byte, error) {
		if pool == nil {
			return nil, fmt.Errorf("voucher hmac: no database pool")
		}
		keys, err := generationKeys(ctx, pool, kr, tenantID, siteID)
		if err != nil {
			return nil, err
		}
		out := make([][]byte, 0, len(keys))
		for _, k := range keys {
			out = append(out, iamv2.VoucherCodeHMAC(k, tenantID, siteID, code))
		}
		return out, nil
	}
}

// generationKeys opens every generation's HMAC key for this owner, newest generation first.
//
// Superseded generations are INCLUDED: supersession stops new issuance under a key, it does not invalidate
// vouchers already indexed with it. They drop out naturally when their vouchers are redeemed or expire.
func generationKeys(ctx context.Context, pool *pgxpool.Pool, kr iamv2.VoucherKeyring,
	tenantID, siteID string) ([][]byte, error) {

	ck := vkKey(tenantID, siteID)
	vkCache.mu.RLock()
	ent, ok := vkCache.m[ck]
	vkCache.mu.RUnlock()
	if ok && time.Since(ent.loadedAt) < 60*time.Second && len(ent.keys) > 0 {
		return ent.keys, nil
	}

	rows, err := pool.Query(ctx, `
	    SELECT generation_no, encryption_key_id::text, hmac_key_ciphertext,
	           COALESCE(aead_params->>'nonce_b64','')
	      FROM iam_v2.voucher_code_key_generations
	     WHERE tenant_id=$1 AND site_id=$2
	     ORDER BY generation_no DESC`, tenantID, siteID)
	if err != nil {
		return nil, fmt.Errorf("voucher hmac: key generations unreadable: %w", err)
	}
	defer rows.Close()
	var keys [][]byte
	for rows.Next() {
		var genNo int
		var keyID, nonceB64 string
		var sealed []byte
		if err := rows.Scan(&genNo, &keyID, &sealed, &nonceB64); err != nil {
			return nil, err
		}
		nonce, derr := decodeB64(nonceB64)
		if derr != nil {
			// One malformed generation must not blind the others: skip it and keep going, so a single bad
			// row cannot lock out every voucher at the site.
			continue
		}
		clear, oerr := iamv2.OpenVoucherHMACKey(kr, keyID, tenantID, siteID, genNo, sealed, nonce)
		if oerr != nil {
			continue
		}
		keys = append(keys, clear)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("voucher hmac: no usable key generation for this site")
	}
	vkCache.mu.Lock()
	vkCache.m[ck] = cachedGenerationKeys{loadedAt: time.Now(), keys: keys}
	vkCache.mu.Unlock()
	return keys, nil
}

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
