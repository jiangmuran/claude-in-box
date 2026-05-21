package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Probe is the result of a connectivity + auth check against an upstream.
type Probe struct {
	OK       bool          `json:"ok"`
	HTTP     int           `json:"http"`
	Endpoint string        `json:"endpoint"`
	Latency  time.Duration `json:"latency_ns"`
	Detail   string        `json:"detail,omitempty"`
}

// ProbeProvider verifies that <api_host>/v1/models accepts the provider's
// API key. Uses GET /v1/models because it does not bill any tokens and
// returns 200 only when both the host is reachable AND the key is valid.
//
//   200       → ok
//   401 / 403 → bad key
//   404       → wrong path / not Anthropic-compatible
//   network   → bad host
func ProbeProvider(ctx context.Context, p Provider) Probe {
	// `?limit=1` keeps the response small on first-party Anthropic. Third
	// parties usually ignore it.
	endpoint := p.APIHost + "/v1/models?limit=1"
	out := Probe{Endpoint: endpoint}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		out.Detail = "build request: " + err.Error()
		return out
	}
	// Send BOTH auth headers — `Authorization: Bearer` for community
	// gateways (claude-code-router, OneAPI, …) and `x-api-key` for direct
	// Anthropic console keys. Upstream uses whichever it recognises;
	// only one needs to validate for a 200.
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 15 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	out.Latency = time.Since(start)
	if err != nil {
		out.Detail = "network: " + err.Error()
		return out
	}
	defer resp.Body.Close()
	out.HTTP = resp.StatusCode

	if resp.StatusCode == 200 {
		out.OK = true
		return out
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case 401, 403:
		out.Detail = fmt.Sprintf("auth rejected (%d): %s", resp.StatusCode, snippet(body))
	case 404:
		out.Detail = "the host returned 404 for /v1/models — is this an Anthropic-compatible endpoint?"
	default:
		out.Detail = fmt.Sprintf("unexpected %d: %s", resp.StatusCode, snippet(body))
	}
	return out
}

func snippet(b []byte) string {
	const cap = 200
	if len(b) > cap {
		return string(b[:cap]) + "…"
	}
	return string(b)
}

// Errors surfaced to callers when the probe itself can't run.
var ErrProbeCancelled = errors.New("providers: probe cancelled")
