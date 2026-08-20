package safehttp

import (
	"net/netip"
	"net/url"
	"testing"
)

func TestPublicIP(t *testing.T) {
	tests := map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "::1": false,
		"fe80::1": false, "2001:db8::1": false,
	}
	for raw, expected := range tests {
		if actual := PublicIP(netip.MustParseAddr(raw)); actual != expected {
			t.Errorf("PublicIP(%s) = %v, want %v", raw, actual, expected)
		}
	}
}

func TestValidateURL(t *testing.T) {
	c, err := New([]string{"https://example.org"}, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for raw, valid := range map[string]bool{"https://example.org/book": true, "http://example.org/book": false, "file:///tmp/book": false, "https://user:pass@example.org/book": false, "https://other.example/book": false} {
		u, _ := url.Parse(raw)
		if got := c.ValidateURL(u) == nil; got != valid {
			t.Errorf("ValidateURL(%q) valid=%v, want %v", raw, got, valid)
		}
	}
}
