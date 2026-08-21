package torrent

import (
	"strings"
	"testing"
)

func TestInspectSingleFile(t *testing.T) {
	inv, err := Inspect(strings.NewReader("d4:infod6:lengthi42e4:name9:test.epub"))
	if err == nil {
		t.Fatal("expected malformed metainfo")
	}
	inv, err = Inspect(strings.NewReader("d4:infod6:lengthi42e4:name9:test.epubee"))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Name != "test.epub" || inv.TotalSize != 42 || len(inv.Files) != 1 {
		t.Fatalf("unexpected inventory: %+v", inv)
	}
}

func TestInspectMultiFile(t *testing.T) {
	data := "d4:infod5:filesld6:lengthi3e4:pathl3:foo4:epubee d6:lengthi4e4:pathl3:bar3:pdfeee4:name4:rootee"
	data = strings.ReplaceAll(data, " ", "")
	inv, err := Inspect(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalSize != 7 || len(inv.Files) != 2 || inv.Files[1].Path != "bar/pdf" {
		t.Fatalf("unexpected inventory: %+v", inv)
	}
}
