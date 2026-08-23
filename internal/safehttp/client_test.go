package safehttp

import (
	"net/netip"
	"net/url"
	"testing"
	"time"
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

func TestAllowsOnlyConfiguredCDNSuffix(t *testing.T) {
	c, err := NewWithSuffixes([]string{"https://covers.openlibrary.org"}, []string{".us.archive.org"}, time.Second, 1024)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _ := url.Parse("https://ia800404.us.archive.org/file.jpg")
	if err = c.ValidateURL(allowed); err != nil {
		t.Fatalf("expected CDN host to be accepted: %v", err)
	}
	for _, raw := range []string{"https://us.archive.org.evil.example/file", "https://evilus.archive.org/file", "http://ia800404.us.archive.org/file"} {
		target, _ := url.Parse(raw)
		if err = c.ValidateURL(target); err == nil {
			t.Fatalf("expected %s to be rejected", raw)
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
