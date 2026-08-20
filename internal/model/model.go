package model

import (
	"context"
	"io"
)

type ProviderInfo struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	SearchAvailable    bool   `json:"searchAvailable"`
	DownloadConfigured bool   `json:"downloadConfigured"`
}

type SearchOptions struct {
	Page     int
	PageSize int
	Format   string
}

type SearchResponse struct {
	ProviderID string `json:"providerId"`
	Query      string `json:"query"`
	Page       int    `json:"page"`
	HasMore    bool   `json:"hasMore"`
	Results    []Book `json:"results"`
}

type Book struct {
	ProviderID  string `json:"providerId"`
	ExternalID  string `json:"externalId"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	ISBN        string `json:"isbn,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`
	Files       []File `json:"files"`
}

type File struct {
	FileID string `json:"fileId"`
	Format string `json:"format"`
	Size   *int64 `json:"size,omitempty"`
	URL    string `json:"url"`
}

type RemoteFile struct {
	URL         string
	ContentType string
	Size        *int64
	Body        io.ReadCloser
}

type ProviderError struct {
	Code      string
	Message   string
	Provider  string
	Retryable bool
	Status    int
}

func (e *ProviderError) Error() string { return e.Message }

type Provider interface {
	Info() ProviderInfo
	Search(context.Context, string, SearchOptions) (SearchResponse, error)
	Details(context.Context, string) (Book, error)
	OpenCover(context.Context, string) (*RemoteFile, error)
	OpenFile(context.Context, string, string) (*RemoteFile, error)
}
