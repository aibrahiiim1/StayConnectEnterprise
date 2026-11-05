package main

// A room grant is one transaction. A deadlock or serialization failure (SQLSTATE 40P01 / 40001) means PostgreSQL rolled all of it
// back: the Auth Context is still unconsumed and no quote, purchase, posting, entitlement, device authorization or session survives.
// So the grant may run once more with nothing to undo, and the guest gets the real answer instead of a generic failure. Any other
// error, including an ambiguous commit, is never retried.

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// grantRetries is how many times a grant that failed retryably runs again.
const grantRetries = 1

type grantRetry struct {
	retryable bool
	final     bool
}

type grantRetryKey struct{}

func withGrantRetry(ctx context.Context, g *grantRetry) context.Context {
	return context.WithValue(ctx, grantRetryKey{}, g)
}

func grantRetryOf(ctx context.Context) *grantRetry {
	g, _ := ctx.Value(grantRetryKey{}).(*grantRetry)
	return g
}

// noteRetry marks the running grant retryable when err is a deadlock or serialization failure.
func noteRetry(ctx context.Context, err error) {
	if g := grantRetryOf(ctx); g != nil && retryableTxError(err) {
		g.retryable = true
	}
}

// retrying is true while this pass will be discarded and run again, so it must leave no record.
func retrying(ctx context.Context) bool {
	g := grantRetryOf(ctx)
	return g != nil && g.retryable && !g.final
}

func retryableTxError(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001")
}

// bufferedResponse holds a pass's answer until it is known not to be retried.
type bufferedResponse struct {
	h    http.Header
	code int
	body bytes.Buffer
}

func newBufferedResponse() *bufferedResponse { return &bufferedResponse{h: http.Header{}} }

func (b *bufferedResponse) Header() http.Header { return b.h }
func (b *bufferedResponse) WriteHeader(code int) {
	if b.code == 0 {
		b.code = code
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.code == 0 {
		b.code = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	for k, v := range b.h {
		w.Header()[k] = v
	}
	if b.code == 0 {
		b.code = http.StatusOK
	}
	w.WriteHeader(b.code)
	_, _ = w.Write(b.body.Bytes())
}

// runRetried runs one pass, and runs it again (at most grantRetries times) when the pass met a retryable transaction failure.
func runRetried(w http.ResponseWriter, r *http.Request, pass func(w http.ResponseWriter, r *http.Request)) {
	for attempt := 0; ; attempt++ {
		g := &grantRetry{final: attempt >= grantRetries}
		buf := newBufferedResponse()
		pass(buf, r.WithContext(withGrantRetry(r.Context(), g)))
		if g.retryable && !g.final {
			continue
		}
		buf.flushTo(w)
		return
	}
}
