package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(t *testing.T, remote string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// The default is to believe nothing: a server exposed directly must not let a
// client pick its own address and dodge the per-address limits.
func TestForwardedHeadersAreIgnoredByDefault(t *testing.T) {
	proxies, err := ParseTrustedProxies("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r := request(t, "203.0.113.9:5000", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	})
	if got := proxies.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("the socket's own address should win, got %q", got)
	}
}

// Behind nginx every connection arrives from loopback, so without this the
// whole internet would count as one client.
func TestForwardedHeaderIsUsedFromATrustedProxy(t *testing.T) {
	proxies, err := ParseTrustedProxies("127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    string
	}{
		{"single hop", "127.0.0.1:5000",
			map[string]string{"X-Forwarded-For": "203.0.113.9"}, "203.0.113.9"},
		{"chain keeps the rightmost untrusted hop", "127.0.0.1:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.7, 203.0.113.9"}, "203.0.113.9"},
		{"skips trusted hops inside the chain", "127.0.0.1:5000",
			map[string]string{"X-Forwarded-For": "203.0.113.9, 10.1.2.3"}, "203.0.113.9"},
		{"falls back to X-Real-IP", "10.0.0.5:5000",
			map[string]string{"X-Real-IP": "203.0.113.9"}, "203.0.113.9"},
		{"garbage falls back to the socket", "127.0.0.1:5000",
			map[string]string{"X-Forwarded-For": "not-an-ip"}, "127.0.0.1"},
		{"no headers at all", "127.0.0.1:5000", nil, "127.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxies.ClientIP(request(t, tc.remote, tc.headers)); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// A forged header from an address that is not a proxy must change nothing.
func TestUntrustedSenderCannotClaimAnAddress(t *testing.T) {
	proxies, err := ParseTrustedProxies("127.0.0.1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	r := request(t, "203.0.113.9:5000", map[string]string{"X-Forwarded-For": "127.0.0.1"})
	if got := proxies.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("a client must not be able to claim another address, got %q", got)
	}
}

func TestParseTrustedProxiesRejectsNonsense(t *testing.T) {
	if _, err := ParseTrustedProxies("127.0.0.1, hello"); err == nil {
		t.Fatal("a typo in the trusted list should be reported, not ignored")
	}
	if _, err := ParseTrustedProxies("  "); err != nil {
		t.Fatalf("an empty list is valid: %v", err)
	}
}
