package providers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ElHefe3/atlas-bridge/internal/cache"
	"github.com/ElHefe3/atlas-bridge/internal/model"
)

func TestAnnaParsesMultipleResults(t *testing.T) {
	html := `<html><body>
<article><a href="/md5/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"><h3>Alice's Adventures in Wonderland</h3></a><span class="author">Lewis Carroll</span><span>EPUB 1.5 MB</span><img src="/covers/alice.jpg"></article>
<article><a href="/md5/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb">The Time Machine</a><span class="author">H. G. Wells</span><span>PDF 2 MB</span></article>
</body></html>`
	a := &Anna{mirrors: []string{"https://annas.example"}}
	books, err := a.parseSearch([]byte(html), "", "https://annas.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("got %d books", len(books))
	}
	if books[0].Files[0].Format != "epub" || books[0].CoverURL != "https://annas.example/covers/alice.jpg" {
		t.Fatalf("unexpected book: %#v", books[0])
	}
}

func TestAnnaSearchFailsOverAfterChallenge(t *testing.T) {
	store, err := cache.Open(filepath.Join(t.TempDir(), "cache.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &sequenceClient{responses: [][]byte{
		[]byte("<html><title>DDoS-Guard</title></html>"),
		[]byte(`<html><body><article><a href="/md5/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">Public Domain Book</a><span>EPUB 1 MB</span><img src="/cover.jpg"></article></body></html>`),
	}}
	a := &Anna{client: client, cache: store, mirrors: []string{"https://first.example", "https://second.example"}}

	result, err := a.Search(context.Background(), "public domain", model.SearchOptions{Page: 1, PageSize: 20})

	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 || len(result.Results) != 1 {
		t.Fatalf("expected second mirror result after challenge; calls=%d results=%d", client.calls, len(result.Results))
	}
	if result.Results[0].CoverURL != "https://second.example/cover.jpg" {
		t.Fatalf("cover did not use successful mirror: %s", result.Results[0].CoverURL)
	}
	if a.challengeActive(time.Now()) {
		t.Fatal("successful failover left challenge circuit open")
	}
}

type sequenceClient struct {
	responses [][]byte
	calls     int
}

func (c *sequenceClient) Do(*http.Request) (*http.Response, error) {
	data := c.responses[c.calls]
	c.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func TestAnnaChallengeCircuitBreaker(t *testing.T) {
	a := &Anna{}
	if a.challengeActive(time.Now()) {
		t.Fatal("new adapter unexpectedly challenged")
	}
	a.markChallenge(time.Now().Add(time.Minute))
	if !a.challengeActive(time.Now()) {
		t.Fatal("challenge circuit did not open")
	}
	a.clearChallenge()
	if a.challengeActive(time.Now()) {
		t.Fatal("challenge circuit did not close")
	}
}

func TestAnnaChallengeSignatures(t *testing.T) {
	for _, value := range []string{"<title>DDoS-Guard</title>", "Checking your browser before accessing", "browser verification"} {
		if !isChallenge([]byte(value)) {
			t.Fatalf("challenge was not detected: %s", value)
		}
	}
	if isChallenge([]byte("<html><title>Search results</title></html>")) {
		t.Fatal("ordinary page detected as challenge")
	}
}
