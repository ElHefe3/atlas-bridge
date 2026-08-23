package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// LocalCatalogue exposes the file-backed catalogue as a normal provider. It
// never performs upstream HTTP; acquisitions are resolved through catalog
// locators by the acquisition manager.
type LocalCatalogue struct {
	Store   *catalog.Store
	Client  requestClient
	Mirrors []string
	Key     string
}

func (p LocalCatalogue) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "anna-local", Name: "Local metadata catalogue", SearchAvailable: true, DownloadConfigured: true}
}
func (p LocalCatalogue) Search(ctx context.Context, q string, o model.SearchOptions) (model.SearchResponse, error) {
	books, more, err := p.Store.Search(ctx, q, o.Page, o.PageSize, o.Format)
	return model.SearchResponse{ProviderID: "anna-local", Query: q, Page: o.Page, HasMore: more, Results: books}, err
}
func (p LocalCatalogue) Details(ctx context.Context, id string) (model.Book, error) {
	b, ok, err := p.Store.GetBook(ctx, "anna-local", id)
	if err != nil {
		return model.Book{}, err
	}
	if !ok {
		return model.Book{}, fmt.Errorf("book not found")
	}
	return b, nil
}
func (p LocalCatalogue) OpenCover(context.Context, string) (*model.RemoteFile, error) {
	return nil, fmt.Errorf("local catalogue has no cover proxy")
}
func (p LocalCatalogue) OpenFile(ctx context.Context, externalID, fileID string) (*model.RemoteFile, error) {
	if p.Store == nil || p.Client == nil || strings.TrimSpace(p.Key) == "" {
		return nil, &model.ProviderError{Code: "acquisition_unconfigured", Message: "local catalogue acquisition is not configured", Provider: "anna-local", Status: http.StatusServiceUnavailable}
	}
	f, ok, err := p.Store.GetFile(ctx, "anna-local", externalID, fileID)
	if err != nil {
		return nil, err
	}
	if !ok || !validMD5(f.MD5) {
		return nil, &model.ProviderError{Code: "locator_unavailable", Message: "no retrieval locator is indexed for this file", Provider: "anna-local", Status: http.StatusNotFound}
	}
	var last error
	for _, mirror := range p.Mirrors {
		target := strings.TrimRight(mirror, "/") + "/dyn/api/fast_download.json?md5=" + url.QueryEscape(strings.ToLower(f.MD5)) + "&key=" + url.QueryEscape(p.Key) + "&path_index=0"
		resp, requestErr := request(ctx, p.Client, target, "application/json")
		if requestErr != nil {
			last = requestErr
			continue
		}
		data, readErr := readBounded(resp.Body, 1<<20)
		resp.Body.Close()
		if readErr != nil {
			last = readErr
			continue
		}
		var payload struct {
			DownloadURL *string `json:"download_url"`
			Error       string  `json:"error"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.DownloadURL == nil || strings.TrimSpace(*payload.DownloadURL) == "" {
			last = &model.ProviderError{Code: "download_resolution_failed", Message: safeUpstreamError(payload.Error), Provider: "anna-local", Retryable: resp.StatusCode >= 500, Status: http.StatusBadGateway}
			continue
		}
		download, downloadErr := request(ctx, p.Client, *payload.DownloadURL, "application/octet-stream")
		if downloadErr != nil {
			last = downloadErr
			continue
		}
		if err = requireOK("anna-local", download); err != nil {
			download.Body.Close()
			last = err
			continue
		}
		return remote(download), nil
	}
	if last == nil {
		last = &model.ProviderError{Code: "download_resolution_failed", Message: "download could not be resolved", Provider: "anna-local", Retryable: true, Status: http.StatusBadGateway}
	}
	return nil, last
}

var localMD5 = regexp.MustCompile(`(?i)^[a-f0-9]{32}$`)

func validMD5(value string) bool { return localMD5.MatchString(strings.TrimSpace(value)) }
