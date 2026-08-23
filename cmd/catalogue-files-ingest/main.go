package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ElHefe3/atlas-bridge/internal/catalog"
)

func main() {
	db := flag.String("db", "", "catalogue SQLite database path")
	input := flag.String("input", "", "zstd-compressed zlib3_files JSONL path")
	max := flag.Int64("max-expanded", 128<<30, "maximum expanded bytes")
	flag.Parse()
	if *db == "" || *input == "" {
		fmt.Fprintln(os.Stderr, "usage: catalogue-files-ingest -db FILE -input FILE.zst")
		os.Exit(2)
	}
	store, err := catalog.Open(*db)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	records, skipped, err := store.IngestZstdFilesJSONL(context.Background(), *input, 0, *max)
	if err != nil {
		fail(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]int{"records": records, "skipped": skipped})
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
