package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the real writer to http.ResponseController. Without it the
// WebSocket upgrade fails with 501: hijacking the connection is impossible
// through a wrapper that hides it.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack forwards to the underlying writer for callers that still reach for the
// interface directly.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("httpapi: ResponseWriter does not support hijacking")
	}
	return h.Hijack()
}

// Flush forwards streaming flushes.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recoverPanics keeps one broken request from taking the process with it. The
// WebSocket handler has its own recovery per frame; this covers the rest.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// A hijacked connection is no longer ours to write to, and this is
			// the normal way a finished WebSocket unwinds. Anything else is a
			// real bug. The type assertion is checked: a panic with a plain
			// string would otherwise panic again, inside the recovery.
			if err, isErr := rec.(error); isErr && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			slog.Error("recovered from a panic in an HTTP handler",
				"path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// secureHeaders applies to everything this server returns. None of it is a web
// app, so the browser is told to assume nothing at all.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Debug("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
