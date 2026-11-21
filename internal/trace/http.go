package trace

import (
	"net/http"
	"strconv"
	"strings"
)

func (t *Tracer) Handler(next http.Handler, route func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote, _ := FromHeader(r.Header)
		ctx := WithTracer(r.Context(), t)
		quiet := r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		var span *Span
		if quiet {
			ctx = WithSpanContext(ctx, SpanContext{TraceID: newID(), SpanID: newSpanID()})
		} else {
			ctx, span = t.begin(ctx, remote, false, r.Method, KindServer, nil)
		}
		w.Header().Set(HeaderTraceID, From(ctx).TraceID.String())
		if span == nil {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))
		pattern := ""
		if route != nil {
			pattern = route(r)
		}
		name, path := routeName(r.Method, pattern)
		span.SetName(name)
		span.SetAttrs(
			String("http.request.method", r.Method),
			String("http.route", path),
			Int("http.response.status_code", sw.status),
		)
		if sw.status >= http.StatusInternalServerError {
			span.Fail("status " + strconv.Itoa(sw.status))
		}
		span.End()
	})
}

func routeName(method, pattern string) (name, path string) {
	if pattern == "" {
		return method + " unmatched", "unmatched"
	}
	if m, p, ok := strings.Cut(pattern, " "); ok && !strings.Contains(m, "/") {
		return pattern, p
	}
	return method + " " + pattern, pattern
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
