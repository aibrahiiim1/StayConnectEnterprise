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

// generationKeyCache avoids re-opening the same sealed key on every login attempt. Keys are held in memory
// only, never logged, and the cache is dropped on restart along with the process.
type generationKeyCache struct {
	mu       sync.RWMutex
	loadedAt time.Time
	keys     [][]byte // clear per-generation HMAC keys, newest generation first
}

var vkCache generationKeyCache

// newGenerationVoucherHMAC returns the VoucherHMAC the authenticator calls. It resolves the ACTIVE
// generation's key and returns that index; the repository looks the index up.
//
// Only the newest non-superseded generation is used to compute the index for a NEW lookup, because that is
// the generation any freshly issued code was indexed under. Older generations remain usable through their
// own stored indexes -- the voucher row already carries the index computed at issuance, so a rotation never
// invalidates an unredeemed voucher.
func newGenerationVoucherHMAC(pool *pgxpool.Pool, kr iamv2.VoucherKeyring) iamv2.VoucherHMAC {
	return func(ctx context.Context, tenantID, siteID, code string) ([]byte, error) {
		if pool == nil {
			return nil, fmt.Errorf("voucher hmac: no database pool")
		}
		key, err := activeGenerationKey(ctx, pool, kr, tenantID, siteID)
		if err != nil {
			return nil, err
		}
		return iamv2.VoucherCodeHMAC(key, tenantID, siteID, code), nil
	}
}

// activeGenerationKey opens the newest non-superseded generation's HMAC key.
func activeGenerationKey(ctx context.Context, pool *pgxpool.Pool, kr iamv2.VoucherKeyring,
	tenantID, siteID string) ([]byte, error) {

	vkCache.mu.RLock()
	fresh := time.Since(vkCache.loadedAt) < 60*time.Second && len(vkCache.keys) > 0
	if fresh {
		k := vkCache.keys[0]
		vkCache.mu.RUnlock()
		return k, nil
	}
	vkCache.mu.RUnlock()

	var genNo int
	var keyID, nonceB64 string
	var sealed []byte
	// One query: generation number, the DEK id that sealed it, the sealed key and its nonce. An earlier
	// draft read the nonce in a second round trip, which could observe a different generation than the first
	// query had chosen if a rotation landed between them.
	if err := pool.QueryRow(ctx, `
	    SELECT generation_no, encryption_key_id::text, hmac_key_ciphertext,
	           COALESCE(aead_params->>'nonce_b64','')
	      FROM iam_v2.voucher_code_key_generations
	     WHERE tenant_id=$1 AND site_id=$2 AND superseded_at IS NULL
	     ORDER BY generation_no DESC
	     LIMIT 1`, tenantID, siteID).Scan(&genNo, &keyID, &sealed, &nonceB64); err != nil {
		return nil, fmt.Errorf("voucher hmac: no usable key generation: %w", err)
	}
	nonce, err := decodeB64(nonceB64)
	if err != nil {
		return nil, fmt.Errorf("voucher hmac: generation nonce malformed: %w", err)
	}
	clear, err := iamv2.OpenVoucherHMACKey(kr, keyID, tenantID, siteID, genNo, sealed, nonce)
	if err != nil {
		return nil, fmt.Errorf("voucher hmac: generation key unopenable: %w", err)
	}
	vkCache.mu.Lock()
	vkCache.keys = [][]byte{clear}
	vkCache.loadedAt = time.Now()
	vkCache.mu.Unlock()
	return clear, nil
}

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
