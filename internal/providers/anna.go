package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/model"
	"github.com/ElHefe3/atlas-bridge/internal/safehttp"
	"github.com/PuerkitoBio/goquery"
)

var annaMD5 = regexp.MustCompile(`(?i)/md5/([a-f0-9]{32})`)
var sizePattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(KB|MB|GB)`)

type Anna struct {
	client  *safehttp.Client
	cache   *cache.Store
	mirrors []string
	key     string
}

func NewAnna(client *safehttp.Client, store *cache.Store, mirrors []string, key string) *Anna {
	return &Anna{client: client, cache: store, mirrors: mirrors, key: strings.TrimSpace(key)}
}

func (a *Anna) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "anna", Name: "Anna's Archive", SearchAvailable: len(a.mirrors) > 0, DownloadConfigured: a.key != ""}
}

func (a *Anna) Search(ctx context.Context, query string, opts model.SearchOptions) (model.SearchResponse, error) {
	var last error
	for _, mirror := range a.mirrors {
		target := strings.TrimRight(mirror, "/") + "/search?q=" + url.QueryEscape(query) + "&page=" + strconv.Itoa(opts.Page)
		resp, err := request(ctx, a.client, target, "text/html,application/xhtml+xml")
		if err != nil {
			last = err
			continue
		}
		data, readErr := readBounded(resp.Body, 10<<20)
		resp.Body.Close()
		if readErr != nil {
			last = readErr
			continue
		}
		if isChallenge(data) {
			last = unavailable("anna", "upstream_challenge", "Anna's Archive requires browser verification on this mirror", true)
			continue
		}
		if err = requireOK("anna", resp); err != nil {
			last = err
			continue
		}
		books, err := a.parseSearch(data, opts.Format)
		if err != nil {
			last = err
			continue
		}
		for i := range books {
			_ = a.cache.Put(books[i])
		}
		return model.SearchResponse{ProviderID: "anna", Query: query, Page: opts.Page, HasMore: len(books) >= opts.PageSize, Results: books}, nil
	}
	if last == nil {
		last = unavailable("anna", "upstream_unavailable", "no Anna's Archive mirror is configured", true)
	}
	return model.SearchResponse{}, last
}

func isChallenge(data []byte) bool {
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "ddos-guard") || strings.Contains(lower, "checking your browser") || strings.Contains(lower, "browser verification")
}

func (a *Anna) parseSearch(data []byte, wantedFormat string) ([]model.Book, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	books := make([]model.Book, 0)
	doc.Find(`a[href*="/md5/"]`).Each(func(_ int, anchor *goquery.Selection) {
		href, _ := anchor.Attr("href")
		match := annaMD5.FindStringSubmatch(href)
		if len(match) != 2 || seen[strings.ToLower(match[1])] {
			return
		}
		container := anchor.ParentsFiltered("div,li,article,tr").First()
		if container.Length() == 0 {
			container = anchor.Parent()
		}
		text := strings.Join(strings.Fields(container.Text()), " ")
		title := strings.TrimSpace(anchor.Text())
		if title == "" {
			title = strings.TrimSpace(container.Find("h3,h2,.text-xl,.text-lg").First().Text())
		}
		if title == "" || len(title) > 500 {
			return
		}
		format := ""
		for _, candidate := range []string{"epub", "pdf"} {
			if strings.Contains(strings.ToLower(text), candidate) {
				format = candidate
				break
			}
		}
		if wantedFormat != "" && format != strings.ToLower(wantedFormat) {
			return
		}
		author := strings.TrimSpace(container.Find(`[class*="author"],a[href*="?q="]`).Last().Text())
		cover, _ := container.Find("img").First().Attr("src")
		id := strings.ToLower(match[1])
		seen[id] = true
		book := model.Book{ProviderID: "anna", ExternalID: id, Title: title, Author: author, CoverURL: absoluteURL(a.mirrors[0], cover), Files: []model.File{}}
		if format != "" {
			book.Files = append(book.Files, model.File{FileID: "0-" + format, Format: format, Size: parseSize(text)})
		}
		books = append(books, book)
	})
	if len(books) == 0 && strings.Contains(strings.ToLower(string(data)), "/md5/") {
		return nil, unavailable("anna", "parse_error", "Anna's Archive result layout was not recognized", false)
	}
	return books, nil
}

func (a *Anna) Details(ctx context.Context, id string) (model.Book, error) {
	if book, ok, err := a.cache.Get("anna", strings.ToLower(id)); err == nil && ok {
		return book, nil
	}
	return model.Book{}, &model.ProviderError{Code: "details_not_cached", Message: "book details are no longer cached; search again", Provider: "anna", Status: http.StatusNotFound}
}

func (a *Anna) OpenCover(ctx context.Context, id string) (*model.RemoteFile, error) {
	book, err := a.Details(ctx, id)
	if err != nil {
		return nil, err
	}
	if book.CoverURL == "" {
		return nil, &model.ProviderError{Code: "cover_not_found", Message: "cover is unavailable", Provider: "anna", Status: http.StatusNotFound}
	}
	resp, err := request(ctx, a.client, book.CoverURL, "image/*")
	if err != nil {
		return nil, err
	}
	if err = requireOK("anna", resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return remote(resp), nil
}

func (a *Anna) OpenFile(ctx context.Context, id, fileID string) (*model.RemoteFile, error) {
	if a.key == "" {
		return nil, &model.ProviderError{Code: "credential_not_configured", Message: "Anna member download key is not configured", Provider: "anna", Status: http.StatusServiceUnavailable}
	}
	parts := strings.SplitN(fileID, "-", 2)
	if len(parts) != 2 || normalizeFormat(parts[1]) == "" {
		return nil, &model.ProviderError{Code: "file_not_found", Message: "unknown file", Provider: "anna", Status: http.StatusNotFound}
	}
	pathIndex, err := strconv.Atoi(parts[0])
	if err != nil || pathIndex < 0 {
		return nil, &model.ProviderError{Code: "file_not_found", Message: "unknown file", Provider: "anna", Status: http.StatusNotFound}
	}
	book, err := a.Details(ctx, id)
	if err != nil {
		return nil, err
	}
	known := false
	for _, file := range book.Files {
		if file.FileID == fileID {
			known = true
			break
		}
	}
	if !known {
		return nil, &model.ProviderError{Code: "file_not_found", Message: "unknown file", Provider: "anna", Status: http.StatusNotFound}
	}
	var last error
	for _, mirror := range a.mirrors {
		target := strings.TrimRight(mirror, "/") + "/dyn/api/fast_download.json?md5=" + url.QueryEscape(id) + "&key=" + url.QueryEscape(a.key) + "&path_index=" + strconv.Itoa(pathIndex)
		resp, err := request(ctx, a.client, target, "application/json")
		if err != nil {
			last = err
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
		if json.Unmarshal(data, &payload) != nil || payload.DownloadURL == nil || *payload.DownloadURL == "" {
			last = &model.ProviderError{Code: "download_resolution_failed", Message: safeUpstreamError(payload.Error), Provider: "anna", Retryable: resp.StatusCode >= 500, Status: http.StatusBadGateway}
			continue
		}
		download, err := request(ctx, a.client, *payload.DownloadURL, "application/octet-stream")
		if err != nil {
			last = err
			continue
		}
		if err = requireOK("anna", download); err != nil {
			download.Body.Close()
			last = err
			continue
		}
		return remote(download), nil
	}
	if last == nil {
		last = unavailable("anna", "download_resolution_failed", "download could not be resolved", true)
	}
	return nil, last
}

func parseSize(text string) *int64 {
	m := sizePattern.FindStringSubmatch(text)
	if len(m) != 3 {
		return nil
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	factor := float64(1 << 10)
	if strings.EqualFold(m[2], "MB") {
		factor = 1 << 20
	}
	if strings.EqualFold(m[2], "GB") {
		factor = 1 << 30
	}
	v := int64(n * factor)
	return &v
}
func absoluteURL(base, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}
func safeUpstreamError(value string) string {
	if strings.TrimSpace(value) == "" {
		return "upstream rejected the download request"
	}
	if len(value) > 200 {
		return "upstream rejected the download request"
	}
	return strings.TrimSpace(value)
}
func remote(resp *http.Response) *model.RemoteFile {
	var size *int64
	if resp.ContentLength >= 0 {
		v := resp.ContentLength
		size = &v
	}
	return &model.RemoteFile{URL: resp.Request.URL.String(), ContentType: resp.Header.Get("Content-Type"), Size: size, Body: resp.Body}
}

var _ = fmt.Sprintf
var _ io.Reader
