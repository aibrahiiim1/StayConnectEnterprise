package main

// Text responses are gzipped for clients that accept it (the sign-in page is large and read over lobby
// Wi-Fi). Images are left alone as already compressed. Cache-Control stays the handler's; Vary tells caches
// the body depends on Accept-Encoding.

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/http"
	"strings"
	"sync"
)

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return w
}}

func compressible(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/json", ct == "application/javascript", ct == "image/svg+xml":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(part, ";")
		if strings.ToLower(strings.TrimSpace(fields[0])) != "gzip" {
			continue
		}
		for _, f := range fields[1:] {
			if q := strings.ReplaceAll(strings.TrimSpace(f), " ", ""); q == "q=0" || q == "q=0.0" || q == "q=0.00" || q == "q=0.000" {
				return false
			}
		}
		return true
	}
	return false
}

func gzipResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || !acceptsGzip(r) || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter decides at the first WriteHeader or Write, once the handler's headers are set.
type gzipResponseWriter struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
}

func (g *gzipResponseWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.ResponseWriter.Header()
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified ||
		status == http.StatusPartialContent || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	gz := gzipWriters.Get().(*gzip.Writer)
	gz.Reset(g.ResponseWriter)
	g.gz = gz
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.decided {
		// Sniff a type as net/http would.
		if g.ResponseWriter.Header().Get("Content-Type") == "" {
			g.ResponseWriter.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) close() {
	if g.gz == nil {
		return
	}
	_ = g.gz.Close()
	g.gz.Reset(nil)
	gzipWriters.Put(g.gz)
	g.gz = nil
}
