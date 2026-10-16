package selfupdate

import (
	"context"
	"crypto/tls"
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

// Source supports public GitHub release assets from one explicitly trusted
// repository. The transport revalidates destination addresses on every dial.
// Redirect queries are bearer-like upstream credentials and never enter errors.
type Source struct {
	trust  Trust
	client *http.Client
}

func NewSource(trust Trust) (*Source, error) {
	if err := trust.Validate(); err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 20 * time.Second, MaxConnsPerHost: 2}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || (host != "github.com" && host != "release-assets.githubusercontent.com") {
			return nil, errors.New("release destination is not permitted")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("release host resolution failed")
		}
		if len(addresses) == 0 {
			return nil, errors.New("release host has no addresses")
		}
		for _, ip := range addresses {
			if !publicAddress(ip) {
				return nil, errors.New("release host resolved to a forbidden destination")
			}
		}
		dialer := net.Dialer{Timeout: 15 * time.Second}
		for _, ip := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("release host connection failed")
	}
	c := &http.Client{Transport: transport, Timeout: 120 * time.Second}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("release redirect limit exceeded")
		}
		if len(via) == 0 {
			return errors.New("invalid release redirect")
		}
		assetSeen := false
		for _, old := range via {
			if old.URL.Hostname() == "release-assets.githubusercontent.com" {
				assetSeen = true
			}
		}
		if err := releaseURL(req.URL.String(), via[0].URL.String(), assetSeen); err != nil {
			return err
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Referer")
		return nil
	}
	return &Source{trust: trust, client: c}, nil
}
func releaseURL(raw, initial string, assetSeen bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Port() != "" || u.Host != u.Hostname() || len(raw) > 8192 {
		return errors.New("release URL is not permitted")
	}
	if u.Host == "github.com" {
		if assetSeen || raw != initial {
			return errors.New("release repository redirect is not permitted")
		}
		return nil
	}
	if u.Host != "release-assets.githubusercontent.com" || !strings.HasPrefix(u.Path, "/") || u.Path == "/" || strings.Contains(u.Path, "\\") {
		return errors.New("release asset host is not permitted")
	}
	return nil
}

var forbiddenNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range forbiddenNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func (s *Source) open(ctx context.Context, tag, name string) (io.ReadCloser, error) {
	if _, err := ParseStable(tag); err != nil {
		return nil, err
	}
	if name != "release.json" && name != "release.sig" && name != archiveName("linux") && name != archiveName("windows") {
		return nil, errors.New("release asset name is not permitted")
	}
	initial := "https://github.com/" + s.trust.Repository + "/releases/download/" + tag + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, initial, nil)
	if err != nil {
		return nil, errors.New("cannot construct release request")
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "minecraft-server-ui-updater/1")
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("release download failed")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("release download returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}
func (s *Source) read(ctx context.Context, tag, name string, max int64) ([]byte, error) {
	r, err := s.open(ctx, tag, name)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, errors.New("release response could not be read")
	}
	if int64(len(b)) > max {
		return nil, errors.New("release response exceeds size limit")
	}
	return b, nil
}
func (s *Source) Metadata(ctx context.Context, tag string) ([]byte, []byte, error) {
	b, err := s.read(ctx, tag, "release.json", MaxManifestBytes)
	if err != nil {
		return nil, nil, err
	}
	sig, err := s.read(ctx, tag, "release.sig", 1024)
	return b, sig, err
}
