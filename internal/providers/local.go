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
	if err == nil {
		for i := range books {
			p.markCover(ctx, &books[i])
		}
	}
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
	p.markCover(ctx, &b)
	return b, nil
}

func (p LocalCatalogue) markCover(ctx context.Context, book *model.Book) {
	if strings.TrimSpace(book.CoverURL) != "" {
		return
	}
	for _, file := range book.Files {
		if indexed, ok, err := p.Store.GetFile(ctx, "anna-local", book.ExternalID, file.FileID); err == nil && ok && validMD5(indexed.MD5) {
			book.CoverURL = "indexed-md5"
			return
		}
	}
}
func (p LocalCatalogue) OpenCover(ctx context.Context, externalID string) (*model.RemoteFile, error) {
	book, err := p.Details(ctx, externalID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, 2)
	if strings.TrimSpace(book.CoverURL) != "" {
		paths = append(paths, book.CoverURL)
	}
	for _, file := range book.Files {
		indexed, ok, lookupErr := p.Store.GetFile(ctx, "anna-local", externalID, file.FileID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if ok && validMD5(indexed.MD5) {
			md5 := strings.ToLower(indexed.MD5)
			paths = append(paths, "/covers/books/"+md5[0:2]+"/"+md5[2:4]+"/"+md5[4:6]+"/"+md5+".jpg")
			break
		}
	}
	var last error
	for _, raw := range paths {
		if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.IsAbs() {
			resp, requestErr := request(ctx, p.Client, raw, "image/*")
			if requestErr == nil && requireOK("anna-local", resp) == nil {
				return remote(resp), nil
			}
			if resp != nil {
				resp.Body.Close()
			}
			last = requestErr
			continue
		}
		for _, mirror := range p.Mirrors {
			target := strings.TrimRight(mirror, "/") + "/" + strings.TrimLeft(raw, "/")
			resp, requestErr := request(ctx, p.Client, target, "image/*")
			if requestErr != nil {
				last = requestErr
				continue
			}
			if requestErr = requireOK("anna-local", resp); requestErr != nil {
				resp.Body.Close()
				last = requestErr
				continue
			}
			return remote(resp), nil
		}
	}
	if last == nil {
		last = fmt.Errorf("cover is unavailable")
	}
	return nil, &model.ProviderError{Code: "cover_not_found", Message: "cover is unavailable", Provider: "anna-local", Status: http.StatusNotFound}
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
