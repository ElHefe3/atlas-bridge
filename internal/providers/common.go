package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/safehttp"
)

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

func request(ctx context.Context, client *safehttp.Client, target, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return client.Do(req)
}

func requireOK(provider string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return &model.ProviderError{Code: "upstream_http_error", Message: fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode), Provider: provider, Retryable: resp.StatusCode >= 500 || resp.StatusCode == 429, Status: http.StatusBadGateway}
}

func readBounded(body io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("response exceeds parsing limit")
	}
	return data, nil
}

func normalizeFormat(value string) string {
	v := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	for _, allowed := range []string{"epub", "pdf"} {
		if v == allowed {
			return v
		}
	}
	return ""
}

func unavailable(provider, code, message string, retryable bool) error {
	status := http.StatusBadGateway
	if code == "upstream_challenge" || code == "upstream_unavailable" {
		status = http.StatusServiceUnavailable
	}
	return &model.ProviderError{Code: code, Message: message, Provider: provider, Retryable: retryable, Status: status}
}
