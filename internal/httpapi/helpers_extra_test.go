package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"clawbench-fleet/internal/fleet"
	"clawbench-fleet/internal/store"
)

func TestQueryIntAndPathID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?n=7&bad=abc", http.NoBody)
	if got := queryInt(req, "n", 1); got != 7 {
		t.Fatalf("query n = %d", got)
	}
	if got := queryInt(req, "bad", 3); got != 3 {
		t.Fatalf("invalid query should use the default, got %d", got)
	}
	if got := queryInt(req, "missing", 5); got != 5 {
		t.Fatalf("missing query = %d", got)
	}
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"12", 12},
		{"0", 0},
		{"-3", 0},
		{"abc", 0},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)
		r.SetPathValue("id", tc.raw)
		id, err := pathID(r)
		if tc.want == 0 {
			if err == nil {
				t.Errorf("%q should be rejected", tc.raw)
			}
			continue
		}
		if err != nil || id != tc.want {
			t.Errorf("%q = %d %v", tc.raw, id, err)
		}
	}
}

func TestClientIPAndOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)
	req.RemoteAddr = "10.1.2.3:5555"
	if got := clientIP(req); got != "10.1.2.3" {
		t.Fatalf("client ip = %q", got)
	}
	req.RemoteAddr = "no-port"
	if got := clientIP(req); got != "no-port" {
		t.Fatalf("client ip = %q", got)
	}

	r := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	r.Host = "fleet.example.com"
	if !sameOrigin(r) {
		t.Fatal("no Origin header should be allowed")
	}
	r.Header.Set("Origin", "https://fleet.example.com")
	if !sameOrigin(r) {
		t.Fatal("matching origin should be allowed")
	}
	r.Header.Set("Origin", "https://evil.example.com")
	if sameOrigin(r) {
		t.Fatal("different origin should be rejected")
	}
	r.Header.Set("Origin", "//bad url")
	if sameOrigin(r) {
		t.Fatal("malformed origin should be rejected")
	}
	if !equalHost("fleet.example.com:443", "fleet.example.com") {
		t.Fatal("default https port should match")
	}
	if !equalHost("FLEET.example.com:80", "fleet.example.com") {
		t.Fatal("host comparison should be case-insensitive")
	}
}

func TestWriteServiceErrMapping(t *testing.T) {
	cases := map[error]int{
		store.ErrNotFound:       http.StatusNotFound,
		store.ErrPortExhausted:  http.StatusConflict,
		fleet.ErrTenantNotFound: http.StatusNotFound,
		errors.New("other"):     http.StatusBadRequest,
	}
	for err, want := range cases {
		rec := httptest.NewRecorder()
		writeServiceErr(rec, err)
		if rec.Code != want {
			t.Errorf("%v = %d, want %d", err, rec.Code, want)
		}
	}
}

func TestRecoverReturns500(t *testing.T) {
	platform := testutilPlatform(t)
	s := New(platform.Svc, platform.Cfg)
	h := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", http.NoBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic = %d", rec.Code)
	}
}

func TestLoginLimiterWindow(t *testing.T) {
	l := newLoginLimiter()
	if blocked, _ := l.blocked("ip"); blocked {
		t.Fatal("fresh limiter should not block")
	}
	for range 5 {
		l.fail("ip")
	}
	blocked, wait := l.blocked("ip")
	if !blocked || wait <= 0 {
		t.Fatalf("blocked = %v wait = %v", blocked, wait)
	}
	for range 20 {
		l.fail("ip")
	}
	// The block never exceeds fifteen minutes.
	if blocked, wait := l.blocked("ip"); !blocked || wait > 16*time.Minute {
		t.Fatalf("backoff = %v %v", blocked, wait)
	}
	l.reset("ip")
	if blocked, _ := l.blocked("ip"); blocked {
		t.Fatal("reset should clear the block")
	}
}

func TestSlidingLimiterPrunesMap(t *testing.T) {
	l := newSlidingLimiter(2, time.Minute)
	first := l.allow("a")
	second := l.allow("a")
	if !first || !second {
		t.Fatal("first two calls should pass")
	}
	if l.allow("a") {
		t.Fatal("third call should be refused")
	}
	// Fill the map past the pruning threshold with stale entries.
	for i := range 4100 {
		l.m[string(rune(i))] = []time.Time{time.Now().Add(-2 * time.Hour)}
	}
	if !l.allow("b") {
		t.Fatal("a fresh key should still be allowed")
	}
	if len(l.m) >= 4100 {
		t.Fatalf("stale keys were not pruned: %d", len(l.m))
	}
}

func TestDecodeJSONLimits(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", stringReader(`{"a":1,"b":2}`))
	var out map[string]int
	rec := httptest.NewRecorder()
	if !decodeJSON(rec, req, &out) {
		t.Fatalf("valid json rejected: %s", rec.Body.String())
	}
	if out["a"] != 1 {
		t.Fatalf("decoded = %v", out)
	}
	rec = httptest.NewRecorder()
	var strict struct {
		A int `json:"a"`
	}
	req = httptest.NewRequest(http.MethodPost, "/x", stringReader(`{"unknown":1}`))
	if decodeJSON(rec, req, &strict) {
		t.Fatal("unknown fields must be rejected")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func stringReader(s string) *readerOf { return &readerOf{s: s} }

type readerOf struct {
	s string
	i int
}

func (r *readerOf) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func TestHeadersMiddleware(t *testing.T) {
	platform := testutilPlatform(t)
	s := New(platform.Svc, platform.Cfg)
	rec := httptest.NewRecorder()
	s.withHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/guest", http.NoBody))
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestContextHelpers(t *testing.T) {
	if adminFrom(httptest.NewRequest(http.MethodGet, "/", http.NoBody)) != nil {
		t.Fatal("no admin should be in a bare request")
	}
	ctx := context.WithValue(context.Background(), adminKey, nil)
	if adminFrom(httptest.NewRequest(http.MethodGet, "/", http.NoBody).WithContext(ctx)) != nil {
		t.Fatal("nil admin should not be returned")
	}
}
