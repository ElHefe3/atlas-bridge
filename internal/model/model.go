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
	MD5    string `json:"md5,omitempty"`
	AACID  string `json:"aacid,omitempty"`
}

// Acquisition is the normalized lifecycle exposed to Atlas. Source-specific
// locators never cross this boundary.
type Acquisition struct {
	ID         string  `json:"id"`
	ProviderID string  `json:"providerId"`
	ExternalID string  `json:"externalId"`
	FileID     string  `json:"fileId"`
	Format     string  `json:"format"`
	Status     string  `json:"status"`
	Progress   float64 `json:"progress"`
	Bytes      int64   `json:"bytesDownloaded"`
	TotalBytes *int64  `json:"totalBytes,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type AcquisitionRequest struct {
	ProviderID string `json:"providerId"`
	ExternalID string `json:"externalId"`
	FileID     string `json:"fileId"`
}

const (
	AcquisitionQueued      = "queued"
	AcquisitionResolving   = "resolving"
	AcquisitionDownloading = "downloading"
	AcquisitionCompleted   = "completed"
	AcquisitionFailed      = "failed"
	AcquisitionCancelled   = "cancelled"
)

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
