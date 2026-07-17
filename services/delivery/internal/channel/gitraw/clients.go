package gitraw

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/delivery/internal/channel"
)

// defaultHTTPClient is the fallback when no client is injected: a bounded-timeout
// client so a hung/slow mirror can never wedge a request indefinitely (the naked
// http.DefaultClient has no timeout).
var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// HTTPTransport is the real RawTransport: an HTTPS GET of base+"/"+path. Bases are
// config-supplied raw hosts (github/gitlab raw, or any mirror); nothing is
// hardcoded here. The HTTP client is injectable for tests.
type HTTPTransport struct {
	HTTP interface {
		Do(*http.Request) (*http.Response, error)
	}
}

// Get fetches path from a single mirror base.
func (t HTTPTransport) Get(ctx context.Context, base, path string) ([]byte, error) {
	url := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	doer := t.HTTP
	var client interface {
		Do(*http.Request) (*http.Response, error)
	} = defaultHTTPClient
	if doer != nil {
		client = doer
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, channel.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitraw: %s status %d", base, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
