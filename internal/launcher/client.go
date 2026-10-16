package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Send verifies PID identity locally and remotely. Delivered means a stdin write,
// never server acknowledgement or completion. Private tokens never leave loopback.
func Send(ctx context.Context, dataDir string, id domain.ProcessIdentity, path string, req Request) (Response, error) {
	p, err := Paths(dataDir, id.InstanceID)
	if err != nil {
		return Response{}, err
	}
	if filepath.Clean(id.WorkingDir) != p.Server {
		return Response{}, fmt.Errorf("identity working directory differs from configured instance")
	}
	if err = VerifyIdentityContext(ctx, id); err != nil {
		return Response{}, err
	}
	var ctl Control
	if err = ReadJSON(filepath.Join(p.State, "control.json"), &ctl); err != nil {
		return Response{}, err
	}
	if ctl.Identity != id {
		return Response{}, fmt.Errorf("launcher identity mismatch")
	}
	u, err := url.Parse(ctl.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return Response{}, fmt.Errorf("unsafe control endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || len(ctl.Token) != 64 {
		return Response{}, fmt.Errorf("invalid control metadata")
	}
	req.Identity = id
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ctl.URL+path, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Authorization", "Bearer "+ctl.Token)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 6 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("control redirects forbidden") }}
	response, err := client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("launcher control: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Response{}, fmt.Errorf("launcher control rejected request: HTTP %d", response.StatusCode)
	}
	var result Response
	if err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result); err != nil {
		return Response{}, err
	}
	return result, nil
}
