package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

type Server struct {
	token      []byte
	publicBase string
	providers  map[string]model.Provider
	logger     *slog.Logger
}

func New(token, publicBase string, providers []model.Provider, logger *slog.Logger) *Server {
	index := make(map[string]model.Provider, len(providers))
	for _, p := range providers {
		index[p.Info().ID] = p
	}
	return &Server{token: []byte(token), publicBase: strings.TrimRight(publicBase, "/"), providers: index, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/providers", s.providersList)
	mux.HandleFunc("GET /v1/providers/{provider}/search", s.search)
	mux.HandleFunc("GET /v1/providers/{provider}/books/{book}", s.details)
	mux.HandleFunc("GET /v1/providers/{provider}/books/{book}/cover", s.cover)
	mux.HandleFunc("GET /v1/providers/{provider}/books/{book}/files/{file}", s.file)
	return s.requestContext(s.authenticate(mux))
}

func (s *Server) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request", "id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(value) != len(s.token) || subtle.ConstantTimeCompare([]byte(value), s.token) != 1 {
			s.writeError(w, r, &model.ProviderError{Code: "unauthorized", Message: "a valid bearer token is required", Status: http.StatusUnauthorized})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	infos := make([]model.ProviderInfo, 0, len(s.providers))
	for _, p := range s.providers {
		infos = append(infos, p.Info())
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "healthy", "providers": infos})
}
func (s *Server) providersList(w http.ResponseWriter, _ *http.Request) {
	infos := make([]model.ProviderInfo, 0, len(s.providers))
	for _, p := range s.providers {
		infos = append(infos, p.Info())
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 3 || len(query) > 500 {
		s.writeError(w, r, bad("invalid_query", "q must contain 3 to 500 characters"))
		return
	}
	page := boundedInt(r.URL.Query().Get("page"), 1, 1, 10000)
	pageSize := boundedInt(r.URL.Query().Get("pageSize"), 20, 1, 50)
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format != "" && format != "epub" && format != "pdf" {
		s.writeError(w, r, bad("invalid_format", "format must be epub or pdf"))
		return
	}
	result, err := p.Search(r.Context(), query, model.SearchOptions{Page: page, PageSize: pageSize, Format: format})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for i := range result.Results {
		s.decorate(&result.Results[i])
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) details(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(w, r)
	if !ok {
		return
	}
	book, err := p.Details(r.Context(), r.PathValue("book"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.decorate(&book)
	writeJSON(w, http.StatusOK, book)
}
func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(w, r)
	if !ok {
		return
	}
	remote, err := p.OpenCover(r.Context(), r.PathValue("book"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer remote.Body.Close()
	stream(w, remote, "inline")
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(w, r)
	if !ok {
		return
	}
	remote, err := p.OpenFile(r.Context(), r.PathValue("book"), r.PathValue("file"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer remote.Body.Close()
	stream(w, remote, "attachment")
}

func (s *Server) provider(w http.ResponseWriter, r *http.Request) (model.Provider, bool) {
	id := strings.ToLower(r.PathValue("provider"))
	p, ok := s.providers[id]
	if !ok {
		s.writeError(w, r, &model.ProviderError{Code: "provider_not_found", Message: "provider was not found", Provider: id, Status: http.StatusNotFound})
		return nil, false
	}
	return p, true
}

func (s *Server) decorate(book *model.Book) {
	base := s.publicBase + "/v1/providers/" + url.PathEscape(book.ProviderID) + "/books/" + url.PathEscape(book.ExternalID)
	if book.CoverURL != "" {
		book.CoverURL = base + "/cover"
	}
	for i := range book.Files {
		book.Files[i].URL = base + "/files/" + url.PathEscape(book.Files[i].FileID)
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	pe := &model.ProviderError{Code: "internal_error", Message: "the request could not be completed", Status: http.StatusInternalServerError}
	if errors.As(err, &pe) {
		if pe.Status == 0 {
			pe.Status = http.StatusBadGateway
		}
	}
	if pe.Status >= 500 {
		s.logger.Warn("request failed", "id", w.Header().Get("X-Request-ID"), "provider", pe.Provider, "code", pe.Code)
	}
	writeJSON(w, pe.Status, map[string]any{"code": pe.Code, "message": pe.Message, "providerId": pe.Provider, "retryable": pe.Retryable, "requestId": w.Header().Get("X-Request-ID")})
}

func bad(code, message string) error {
	return &model.ProviderError{Code: code, Message: message, Status: http.StatusBadRequest}
}
func boundedInt(raw string, fallback, min, max int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}
func requestID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func stream(w http.ResponseWriter, remote *model.RemoteFile, disposition string) {
	contentType := remote.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "no-store")
	if remote.Size != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*remote.Size, 10))
	}
	_, _ = io.Copy(w, remote.Body)
}
