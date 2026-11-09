package providers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ElHefe3/atlas-bridge/internal/model"
)

// The CDN's real limit page (2026-11-09), returned with status 500 and sometimes 200.
const libgenLimitPage = `<h1 style="color:red">Error</h1><p>You have downloaded too much files (50) in the last 300 seconds, please wait</p>`

func classify(t *testing.T, status int, page string) *model.ProviderError {
	t.Helper()
	var pe *model.ProviderError
	if !errors.As(libgenErrorPage(status, strings.NewReader(page)), &pe) {
		t.Fatalf("not a ProviderError")
	}
	return pe
}

func TestLibGenRateLimitPageIsRetryableUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusOK, http.StatusTooManyRequests} {
		page := libgenLimitPage
		if status == http.StatusTooManyRequests {
			page = ""
		}
		pe := classify(t, status, page)
		if pe.Code != "upstream_rate_limited" || !pe.Retryable || pe.Status != http.StatusServiceUnavailable {
			t.Errorf("status %d: got %+v", status, pe)
		}
		if !strings.Contains(pe.Message, "try again in a few minutes") {
			t.Errorf("status %d: message %q", status, pe.Message)
		}
	}
}

func TestLibGenOtherErrorPagesKeepAShortExcerpt(t *testing.T) {
	pe := classify(t, http.StatusNotFound, `<html><body><h1>Not&nbsp;Found</h1><p>File is missing</p></body></html>`)
	if pe.Code != "upstream_http_error" || pe.Retryable || !strings.Contains(pe.Message, "HTTP 404: Not Found File is missing") {
		t.Fatalf("got %+v", pe)
	}

	pe = classify(t, http.StatusOK, "<p>"+strings.Repeat("word ", 100)+"</p>")
	if pe.Code != "invalid_upstream_file" || !strings.Contains(pe.Message, "it returned a page:") || len(pe.Message) > 260 {
		t.Fatalf("got %+v (len %d)", pe, len(pe.Message))
	}

	pe = classify(t, http.StatusOK, "")
	if pe.Code != "invalid_upstream_file" || pe.Message != "LibGen did not return the requested book file" {
		t.Fatalf("empty body: got %+v", pe)
	}
}
