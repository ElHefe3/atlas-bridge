package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const MetadataIP = "169.254.169.254"

type Client struct {
	http    *http.Client
	allowed map[string]struct{}
	maxBody int64
}

func New(origins []string, timeout time.Duration, maxBody int64) (*Client, error) {
	allowed := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, fmt.Errorf("invalid HTTPS origin %q", raw)
		}
		allowed[strings.ToLower(u.Scheme+"://"+u.Host)] = struct{}{}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !PublicIP(ip) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("destination resolved only to blocked addresses")
	}
	c := &Client{allowed: allowed, maxBody: maxBody}
	c.http = &http.Client{Transport: transport}
	c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("redirect limit exceeded")
		}
		return c.ValidateURL(req.URL)
	}
	return c, nil
}

func PublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.String() == MetadataIP {
		return false
	}
	if ip.Is4() {
		blocked := []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}
		for _, raw := range blocked {
			p := netip.MustParsePrefix(raw)
			if p.Contains(ip) {
				return false
			}
		}
	} else {
		blocked := []string{"::/128", "2001:db8::/32", "2001:10::/28", "fc00::/7", "fe80::/10"}
		for _, raw := range blocked {
			p := netip.MustParsePrefix(raw)
			if p.Contains(ip) {
				return false
			}
		}
	}
	return true
}

func (c *Client) ValidateURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return errors.New("only credential-free HTTPS URLs are permitted")
	}
	if _, ok := c.allowed[strings.ToLower(u.Scheme+"://"+u.Host)]; !ok {
		return fmt.Errorf("origin %s is not allowed", u.Host)
	}
	return nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := c.ValidateURL(req.URL); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > c.maxBody && c.maxBody > 0 {
		resp.Body.Close()
		return nil, errors.New("response exceeds configured size limit")
	}
	if c.maxBody > 0 {
		idleBody := &idleReadCloser{body: resp.Body, timeout: time.Minute}
		resp.Body = &limitedReadCloser{Reader: io.LimitReader(idleBody, c.maxBody+1), Closer: idleBody, limit: c.maxBody}
	}
	return resp, nil
}

type idleReadCloser struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	timer := time.AfterFunc(r.timeout, func() { _ = r.body.Close() })
	n, err := r.body.Read(p)
	_ = timer.Stop()
	return n, err
}

func (r *idleReadCloser) Close() error { return r.body.Close() }

type limitedReadCloser struct {
	io.Reader
	io.Closer
	limit int64
	read  int64
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	n, err := l.Reader.Read(p)
	l.read += int64(n)
	if l.read > l.limit {
		return n, errors.New("response exceeds configured size limit")
	}
	return n, err
}
