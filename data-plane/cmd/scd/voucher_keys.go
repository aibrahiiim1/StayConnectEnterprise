package main

// Resolves the voucher blind-index key from its GENERATION: authentication cannot know which key indexed a submitted code, so it computes the index
// under every usable generation and lets the repository match. That is what keeps old, unredeemed vouchers authenticating after a rotation.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Opened blind-index keys for one (tenant, site), KEYED BY OWNER: a global cache would index codes under the WRONG site's key on a multi-site appliance and
// every other site's vouchers would read as invalid. Keys are held in memory only and never logged.
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

// Drops this owner's cached generation keys. Minting a generation makes the cache WRONG, not merely stale: a code under the new key would read as
// invalid_code until the TTL. Issuance invalidates directly; the TTL is the backstop for a generation minted by another process.
func invalidateVoucherKeyCache(tenantID, siteID string) {
	vkCache.mu.Lock()
	delete(vkCache.m, vkKey(tenantID, siteID))
	vkCache.mu.Unlock()
}

// Returns ONE INDEX PER USABLE GENERATION, newest first, including superseded ones: a voucher keeps its original generation index forever, so returning
// only the active key would invalidate every unredeemed voucher at rotation.
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

// Opens every generation's HMAC key, newest first, superseded ones INCLUDED: supersession stops new issuance, it does not invalidate vouchers already indexed.
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
			// One malformed generation must not blind the others: skip it so a bad row cannot lock out every voucher at the site.
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
