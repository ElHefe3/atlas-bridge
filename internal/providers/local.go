package providers

import (
	"context"
	"fmt"
	"github.com/ElHefe3/atlas-bridge/internal/catalog"
	"github.com/ElHefe3/atlas-bridge/internal/model"
)

// LocalCatalogue exposes the file-backed catalogue as a normal provider. It
// never performs upstream HTTP; acquisitions are resolved through catalog
// locators by the acquisition manager.
type LocalCatalogue struct{ Store *catalog.Store }

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
func (p LocalCatalogue) OpenFile(context.Context, string, string) (*model.RemoteFile, error) {
	return nil, fmt.Errorf("use an acquisition for local catalogue files")
}
