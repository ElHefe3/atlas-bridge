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

// LocalCatalogue exposes the file-backed catalogue as a normal provider.
// Metadata search is local; bounded upstream requests are used only for cover
// enrichment and deterministic acquisition resolution.
type LocalCatalogue struct {
	Store          *catalog.Store
	Client         requestClient
	MetadataClient requestClient
	Mirrors        []string
	Key            string
}

func (p LocalCatalogue) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "anna-local", Name: "Local metadata catalogue", SearchAvailable: true, DownloadConfigured: true}
}
func (p LocalCatalogue) Search(ctx context.Context, q string, o model.SearchOptions) (model.SearchResponse, error) {
	books, more, err := p.Store.Search(ctx, q, o.Page, o.PageSize, o.Format)
	if err == nil {
		for i := range books {
			if err = p.prepareBook(ctx, &books[i]); err != nil {
				break
			}
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
	if err = p.prepareBook(ctx, &b); err != nil {
		return model.Book{}, err
	}
	return b, nil
}

// prepareBook only advertises files that have a deterministic acquisition
// route. This prevents Atlas from presenting an Add action which can only fail
// later with locator_unavailable.
func (p LocalCatalogue) prepareBook(ctx context.Context, book *model.Book) error {
	files := make([]model.File, 0, len(book.Files))
	hasIndexedCover := false
	for _, file := range book.Files {
		indexed, ok, err := p.Store.GetFile(ctx, "anna-local", book.ExternalID, file.FileID)
		if err != nil {
			return err
		}
		if ok && validMD5(indexed.MD5) {
			hasIndexedCover = true
			if strings.TrimSpace(p.Key) != "" {
				file.MD5, file.AACID = indexed.MD5, indexed.AACID
				files = append(files, file)
				continue
			}
		}
		locator, locatorOK, err := p.Store.GetLocator(ctx, "anna-local", book.ExternalID, file.FileID)
		if err != nil {
			return err
		}
		if locatorOK && locator.Kind == catalog.LocatorTorrentFile && strings.TrimSpace(locator.Metainfo) != "" && strings.TrimSpace(locator.Path) != "" {
			files = append(files, file)
		}
	}
	book.Files = files
	if strings.TrimSpace(book.CoverURL) == "" {
		if p.MetadataClient != nil {
			book.CoverURL = "metadata-search"
		} else if hasIndexedCover {
			book.CoverURL = "indexed-md5"
		}
	}
	return nil
}
func (p LocalCatalogue) OpenCover(ctx context.Context, externalID string) (*model.RemoteFile, error) {
	book, ok, err := p.Store.GetBook(ctx, "anna-local", externalID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("book not found")
	}
	paths := make([]string, 0, 2)
	if strings.TrimSpace(book.CoverURL) != "" && book.CoverURL != "indexed-md5" && book.CoverURL != "metadata-search" {
		paths = append(paths, book.CoverURL)
	}
	var last error
	for _, raw := range paths {
		if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.IsAbs() {
			client := p.Client
			if strings.HasSuffix(strings.ToLower(parsed.Hostname()), "openlibrary.org") && p.MetadataClient != nil {
				client = p.MetadataClient
			}
			if client == nil {
				continue
			}
			resp, requestErr := request(ctx, client, raw, "image/*")
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
	if cover, coverErr := p.openMetadataCover(ctx, book); coverErr == nil {
		return cover, nil
	} else {
		last = coverErr
	}
	if last == nil {
		last = fmt.Errorf("cover is unavailable")
	}
	return nil, &model.ProviderError{Code: "cover_not_found", Message: "cover is unavailable", Provider: "anna-local", Status: http.StatusNotFound}
}

func (p LocalCatalogue) openMetadataCover(ctx context.Context, book model.Book) (*model.RemoteFile, error) {
	if p.MetadataClient == nil || strings.TrimSpace(book.Title) == "" {
		return nil, fmt.Errorf("metadata cover lookup is unavailable")
	}
	title := metadataQuery(book.Title)
	author := metadataQuery(book.Author)
	values := url.Values{"title": []string{title}, "limit": []string{"5"}, "fields": []string{"cover_i"}}
	if author != "" && !strings.EqualFold(author, "unknown") && !strings.EqualFold(author, "unknown author") {
		values.Set("author", author)
	}
	resp, err := request(ctx, p.MetadataClient, "https://openlibrary.org/search.json?"+values.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	if err = requireOK("anna-local", resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	data, err := readBounded(resp.Body, 1<<20)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var result struct {
		Docs []struct {
			CoverID int64 `json:"cover_i"`
		} `json:"docs"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var coverID int64
	for _, doc := range result.Docs {
		if doc.CoverID > 0 {
			coverID = doc.CoverID
			break
		}
	}
	if coverID <= 0 {
		return nil, fmt.Errorf("metadata cover was not found")
	}
	coverURL := fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", coverID)
	cover, err := request(ctx, p.MetadataClient, coverURL, "image/*")
	if err != nil {
		return nil, err
	}
	if err = requireOK("anna-local", cover); err != nil {
		cover.Body.Close()
		return nil, err
	}
	_ = p.Store.SetCoverURL(ctx, "anna-local", book.ExternalID, coverURL)
	return remote(cover), nil
}

func metadataQuery(value string) string {
	value = strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSpace(value))
	return strings.Join(strings.Fields(value), " ")
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
