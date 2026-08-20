package providers

import "testing"

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
