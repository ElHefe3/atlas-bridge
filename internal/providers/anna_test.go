package providers

import (
	"testing"
	"time"
)

func TestAnnaParsesMultipleResults(t *testing.T) {
	html := `<html><body>
<article><a href="/md5/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"><h3>Alice's Adventures in Wonderland</h3></a><span class="author">Lewis Carroll</span><span>EPUB 1.5 MB</span><img src="/covers/alice.jpg"></article>
<article><a href="/md5/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb">The Time Machine</a><span class="author">H. G. Wells</span><span>PDF 2 MB</span></article>
</body></html>`
	a := &Anna{mirrors: []string{"https://annas.example"}}
	books, err := a.parseSearch([]byte(html), "")
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
