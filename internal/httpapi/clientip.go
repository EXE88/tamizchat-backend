package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxies decides whether forwarded headers may be believed.
//
// This matters more than it looks. Behind nginx every connection arrives from
// 127.0.0.1, so a per-address limit would count the whole internet as one
// client. In front of nginx, believing X-Forwarded-For would let anyone claim
// any address and dodge the same limit. So the header is read only when the
// connection itself came from an address the operator listed.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies reads a comma separated list of addresses or CIDR blocks.
// An empty list means no header is ever trusted, which is the safe default for
// a server exposed directly.
func ParseTrustedProxies(raw string) (*TrustedProxies, error) {
	t := &TrustedProxies{}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if _, block, err := net.ParseCIDR(entry); err == nil {
			t.nets = append(t.nets, block)
			continue
		}

		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, &net.ParseError{Type: "trusted proxy address", Text: entry}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		t.nets = append(t.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return t, nil
}

// Trusts reports whether an address is one of the configured proxies.
func (t *TrustedProxies) Trusts(addr string) bool {
	if t == nil || len(t.nets) == 0 {
		return false
	}
	ip := net.ParseIP(hostOf(addr))
	if ip == nil {
		return false
	}
	for _, block := range t.nets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to hold responsible for a request: the socket's
// own address, or the one a trusted proxy reported.
func (t *TrustedProxies) ClientIP(r *http.Request) string {
	remote := hostOf(r.RemoteAddr)
	if !t.Trusts(r.RemoteAddr) {
		return remote
	}

	// X-Forwarded-For is a chain: client, proxy1, proxy2. The rightmost entry
	// was added by the proxy we trust, so walk right to left and take the first
	// address that is not itself a trusted proxy — anything further left was
	// written by someone we have no reason to believe.
	if chain := r.Header.Get("X-Forwarded-For"); chain != "" {
		parts := strings.Split(chain, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" || net.ParseIP(candidate) == nil {
				continue
			}
			if t.Trusts(candidate) {
				continue
			}
			return candidate
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" && net.ParseIP(real) != nil {
		return real
	}
	return remote
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}
