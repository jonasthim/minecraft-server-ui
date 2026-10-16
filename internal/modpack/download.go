package modpack

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Private transport consumes only URLs read from an already hash-verified mrpack.
// Redirect destinations pass the same exact host policy; credentials never flow.
func allowedPackURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawQuery != "" || strings.ContainsAny(raw, " \r\n\t\\") {
		return nil, invalid("unsafe pack-file URL")
	}
	switch u.Host {
	case "cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com":
	default:
		return nil, invalid("pack-file host is outside exact spec whitelist")
	}
	for _, part := range strings.Split(u.Path, "/") {
		decoded, e := url.PathUnescape(part)
		if e != nil || decoded == ".." || strings.ContainsAny(decoded, "\\\x00") {
			return nil, invalid("unsafe pack-file URL path")
		}
	}
	return u, nil
}
func publicPackIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}
func packHTTPClient() *http.Client {
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || port != "443" {
			return nil, invalid("unsafe pack destination")
		}
		if _, e = allowedPackURL("https://" + host + "/"); e != nil {
			return nil, e
		}
		addresses, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		if len(addresses) == 0 {
			return nil, invalid("pack host has no address")
		}
		for _, ip := range addresses {
			if !publicPackIP(ip) {
				return nil, invalid("pack host resolves to a nonpublic address")
			}
		}
		var last error
		for _, ip := range addresses {
			connection, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return connection, nil
			}
			last = e
		}
		return nil, last
	}}
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return invalid("pack redirect limit exceeded")
		}
		if _, e := allowedPackURL(req.URL.String()); e != nil {
			return e
		}
		req.Header = make(http.Header)
		req.Header.Set("User-Agent", "MinecraftServerUI/0.1 (server pack files)")
		return nil
	}}
}
func (s *Service) downloadPackFile(ctx context.Context, id string, actor int64, file PackFile) (stage, error) {
	return s.downloadPackFileUsing(ctx, id, actor, file, packHTTPClient())
}

func (s *Service) downloadPackFileUsing(ctx context.Context, id string, actor int64, file PackFile, client *http.Client) (stage, error) {
	if e := validateDestinationSize(file.Path, file.Size); e != nil {
		return stage{}, e
	}
	var result error
	for _, raw := range file.Downloads {
		u, e := allowedPackURL(raw)
		if e != nil {
			result = e
			continue
		}
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if e != nil {
			return stage{}, e
		}
		request.Header.Set("User-Agent", "MinecraftServerUI/0.1 (server pack files)")
		response, e := client.Do(request)
		if e != nil {
			result = e
			continue
		}
		if response.StatusCode != http.StatusOK || response.ContentLength > file.Size {
			response.Body.Close()
			result = invalid("pack file unavailable or oversized")
			continue
		}
		hashes, e := publishedHashes(file.Hashes)
		if e != nil {
			response.Body.Close()
			return stage{}, e
		}
		writers := []io.Writer{}
		for _, h := range hashes {
			writers = append(writers, h)
		}
		st, e := s.stage(ctx, id, actor, io.TeeReader(response.Body, io.MultiWriter(writers...)), file.Size)
		response.Body.Close()
		if e != nil {
			result = e
			continue
		}
		for name, h := range hashes {
			if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), file.Hashes[name]) {
				return stage{}, invalid("pack member published digest mismatch")
			}
		}
		if e = s.validateDestinationStage(ctx, id, file.Path, st); e != nil {
			return stage{}, e
		}
		return st, nil
	}
	if result == nil {
		result = invalid("pack member has no permitted download")
	}
	return stage{}, result
}
