package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ElHefe3/atlas-bridge/internal/torrent"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: catalogue-inspect FILE.torrent")
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	inventory, err := torrent.Inspect(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inventory); err != nil {
		os.Exit(1)
	}
}
