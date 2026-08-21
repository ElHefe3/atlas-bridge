package providers

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestLibGenParsesClassicTable(t *testing.T) {
	html := `<table class="c"><tr><th>ID</th></tr><tr>
<td>1</td><td>Jane Austen</td><td>Pride and Prejudice</td><td>Public Domain</td><td>1813</td><td>300</td><td>English</td><td>1 MB</td><td>epub</td><td><a href="https://library.lol/main/cccccccccccccccccccccccccccccccc">[1]</a></td><td></td>
</tr></table>`
	l := &LibGen{}
	books, err := l.parseSearch([]byte(html), "https://libgen.example", "epub")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d", len(books))
	}
	if books[0].Title != "Pride and Prejudice" || books[0].Files[0].Format != "epub" {
		t.Fatalf("unexpected book: %#v", books[0])
	}
}

func TestLibGenParsesCurrentTableTitleInsteadOfPublisher(t *testing.T) {
	html := `<table><tr>
<td><b>Series</b><br><a href="edition.php?id=1">The 48 Laws of Power</a></td><td>Greene, Robert</td><td>Penguin</td><td>1998</td><td>English</td><td>480 pages</td><td>2 MB</td><td>epub</td><td><a href="/ads.php?md5=dddddddddddddddddddddddddddddddd">1</a></td>
</tr></table>`
	l := &LibGen{}
	books, err := l.parseSearch([]byte(html), "https://libgen.example", "epub")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].Title != "The 48 Laws of Power" || books[0].Author != "Greene, Robert" {
		t.Fatalf("unexpected book: %#v", books)
	}
}

func TestLibGenDownloadResolutionIgnoresNavigationDownloadAnchor(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><body>
<a href="#">DOWNLOAD</a>
<a href="get.php?md5=dddddddddddddddddddddddddddddddd&amp;key=test"><h2>GET</h2></a>
</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	got := resolveLibGenDownload(doc, "https://libgen.example/ads.php?md5=dddddddddddddddddddddddddddddddd", "pdf")
	if got != "https://libgen.example/get.php?md5=dddddddddddddddddddddddddddddddd&key=test" {
		t.Fatalf("unexpected download URL: %s", got)
	}
}
