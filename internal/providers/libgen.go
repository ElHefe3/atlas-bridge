package providers

import (
	"bytes"
	"context"
	"fmt"
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

var md5Pattern = regexp.MustCompile(`(?i)(?:md5=|/main/|/book/)([a-f0-9]{32})`)

type LibGen struct {
	client  *safehttp.Client
	cache   *cache.Store
	mirrors []string
}

func NewLibGen(client *safehttp.Client, store *cache.Store, mirrors []string) *LibGen {
	return &LibGen{client: client, cache: store, mirrors: mirrors}
}
func (l *LibGen) Info() model.ProviderInfo {
	return model.ProviderInfo{ID: "libgen", Name: "Library Genesis", SearchAvailable: len(l.mirrors) > 0, DownloadConfigured: true}
}

func (l *LibGen) Search(ctx context.Context, query string, opts model.SearchOptions) (model.SearchResponse, error) {
	var last error
	for _, mirror := range l.mirrors {
		target := strings.TrimRight(mirror, "/") + "/search.php?req=" + url.QueryEscape(query) + "&open=0&res=" + strconv.Itoa(opts.PageSize) + "&view=simple&phrase=1&column=def&page=" + strconv.Itoa(opts.Page)
		resp, err := request(ctx, l.client, target, "text/html,application/xhtml+xml")
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
		if err = requireOK("libgen", resp); err != nil {
			last = err
			continue
		}
		books, err := l.parseSearch(data, mirror, opts.Format)
		if err != nil {
			last = err
			continue
		}
		for i := range books {
			_ = l.cache.Put(books[i])
		}
		return model.SearchResponse{ProviderID: "libgen", Query: query, Page: opts.Page, HasMore: len(books) >= opts.PageSize, Results: books}, nil
	}
	if last == nil {
		last = unavailable("libgen", "upstream_unavailable", "no LibGen mirror is configured", true)
	}
	return model.SearchResponse{}, last
}

func (l *LibGen) parseSearch(data []byte, mirror, wantedFormat string) ([]model.Book, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	books := make([]model.Book, 0)
	seen := map[string]bool{}
	doc.Find("table.c tr, table tr").Each(func(_ int, row *goquery.Selection) {
		cells := row.Find("td")
		if cells.Length() < 8 {
			return
		}
		var id string
		var detail string
		row.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
			href, _ := a.Attr("href")
			m := md5Pattern.FindStringSubmatch(href)
			if len(m) == 2 {
				id = strings.ToLower(m[1])
				detail = absoluteURL(mirror, href)
				return false
			}
			return true
		})
		if id == "" || seen[id] {
			return
		}
		title := strings.TrimSpace(cells.Eq(2).Text())
		author := strings.TrimSpace(cells.Eq(1).Text())
		if title == "" {
			title = strings.TrimSpace(row.Find("a[title],a[href]").First().Text())
		}
		format := normalizeFormat(cells.Eq(cells.Length() - 1).Text())
		if format == "" {
			for _, f := range []string{"epub", "pdf"} {
				if strings.Contains(strings.ToLower(row.Text()), f) {
					format = f
					break
				}
			}
		}
		if wantedFormat != "" && format != strings.ToLower(wantedFormat) {
			return
		}
		if title == "" || format == "" {
			return
		}
		seen[id] = true
		cover, _ := row.Find("img").First().Attr("src")
		book := model.Book{ProviderID: "libgen", ExternalID: id, Title: title, Author: author, CoverURL: absoluteURL(mirror, cover), Files: []model.File{{FileID: "0-" + format, Format: format, Size: parseSize(row.Text()), URL: detail}}}
		books = append(books, book)
	})
	if len(books) == 0 {
		return nil, unavailable("libgen", "parse_error", "LibGen returned no recognizable search results", false)
	}
	return books, nil
}

func (l *LibGen) Details(ctx context.Context, id string) (model.Book, error) {
	if book, ok, err := l.cache.Get("libgen", strings.ToLower(id)); err == nil && ok {
		return book, nil
	}
	return model.Book{}, &model.ProviderError{Code: "details_not_cached", Message: "book details are no longer cached; search again", Provider: "libgen", Status: http.StatusNotFound}
}

func (l *LibGen) OpenCover(ctx context.Context, id string) (*model.RemoteFile, error) {
	book, err := l.Details(ctx, id)
	if err != nil {
		return nil, err
	}
	if book.CoverURL == "" {
		return nil, &model.ProviderError{Code: "cover_not_found", Message: "cover is unavailable", Provider: "libgen", Status: http.StatusNotFound}
	}
	resp, err := request(ctx, l.client, book.CoverURL, "image/*")
	if err != nil {
		return nil, err
	}
	if err = requireOK("libgen", resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return remote(resp), nil
}

func (l *LibGen) OpenFile(ctx context.Context, id, fileID string) (*model.RemoteFile, error) {
	book, err := l.Details(ctx, id)
	if err != nil {
		return nil, err
	}
	var detail string
	for _, file := range book.Files {
		if file.FileID == fileID {
			detail = file.URL
			break
		}
	}
	if detail == "" {
		return nil, &model.ProviderError{Code: "file_not_found", Message: "unknown file", Provider: "libgen", Status: http.StatusNotFound}
	}
	resp, err := request(ctx, l.client, detail, "text/html,application/xhtml+xml")
	if err != nil {
		return nil, err
	}
	data, readErr := readBounded(resp.Body, 5<<20)
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if err = requireOK("libgen", resp); err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var download string
	doc.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		href, _ := a.Attr("href")
		label := strings.ToLower(strings.TrimSpace(a.Text()))
		if label == "get" || strings.Contains(label, "download") || strings.HasSuffix(strings.ToLower(strings.Split(href, "?")[0]), "."+book.Files[0].Format) {
			download = absoluteURL(detail, href)
			return false
		}
		return true
	})
	if download == "" {
		return nil, unavailable("libgen", "download_resolution_failed", "LibGen download link was not found", false)
	}
	fileResp, err := request(ctx, l.client, download, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	if err = requireOK("libgen", fileResp); err != nil {
		fileResp.Body.Close()
		return nil, err
	}
	return remote(fileResp), nil
}

var _ = fmt.Sprintf
