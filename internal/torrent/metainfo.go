package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// FileEntry is an inventory item from torrent metainfo. It does not download
// or contact peers.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type Inventory struct {
	Name      string      `json:"name"`
	InfoHash  string      `json:"infoHash"`
	TotalSize int64       `json:"totalSize"`
	Files     []FileEntry `json:"files"`
}

// Inspect reads a bencoded .torrent descriptor only. It intentionally does
// not accept magnet links because magnets do not contain file inventories.
func Inspect(r io.Reader) (Inventory, error) {
	value, err := decode(r)
	if err != nil {
		return Inventory{}, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Inventory{}, fmt.Errorf("torrent root is not a dictionary")
	}
	info, ok := root["info"].(map[string]any)
	if !ok {
		return Inventory{}, fmt.Errorf("torrent has no info dictionary")
	}
	name, _ := info["name"].(string)
	var files []FileEntry
	if list, ok := info["files"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return Inventory{}, fmt.Errorf("invalid torrent file entry")
			}
			n, ok := m["length"].(int64)
			if !ok || n < 0 {
				return Inventory{}, fmt.Errorf("invalid torrent file length")
			}
			parts, ok := m["path"].([]any)
			if !ok {
				return Inventory{}, fmt.Errorf("invalid torrent file path")
			}
			path := ""
			for i, p := range parts {
				s, ok := p.(string)
				if !ok || s == "" {
					return Inventory{}, fmt.Errorf("invalid torrent path component")
				}
				if i > 0 {
					path += "/"
				}
				path += s
			}
			files = append(files, FileEntry{Path: path, Size: n})
		}
	} else {
		n, ok := info["length"].(int64)
		if !ok || n < 0 {
			return Inventory{}, fmt.Errorf("torrent has no valid files")
		}
		files = []FileEntry{{Path: name, Size: n}}
	}
	var total int64
	for _, f := range files {
		if total > (1<<63-1)-f.Size {
			return Inventory{}, fmt.Errorf("torrent size overflow")
		}
		total += f.Size
	}
	encoded, err := encode(info)
	if err != nil {
		return Inventory{}, err
	}
	hash := sha1.Sum(encoded)
	return Inventory{Name: name, InfoHash: hex.EncodeToString(hash[:]), TotalSize: total, Files: files}, nil
}

func decode(r io.Reader) (any, error) { return readValue(&reader{r: r}) }

type reader struct{ r io.Reader }

func (r *reader) readByte() (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r.r, b[:])
	return b[0], err
}
func (r *reader) readUntil(delim byte) ([]byte, error) {
	var out []byte
	for {
		b, err := r.readByte()
		if err != nil {
			return nil, err
		}
		if b == delim {
			return out, nil
		}
		out = append(out, b)
		if len(out) > 1024*1024 {
			return nil, fmt.Errorf("bencode token too large")
		}
	}
}
func readValue(r *reader) (any, error) {
	b, err := r.readByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case 'i':
		raw, e := r.readUntil('e')
		if e != nil {
			return nil, e
		}
		n, e := strconv.ParseInt(string(raw), 10, 64)
		return n, e
	case 'l':
		var out []any
		for {
			b, e := r.readByte()
			if e != nil {
				return nil, e
			}
			if b == 'e' {
				return out, nil
			}
			if err := unread(r, b); err != nil {
				return nil, err
			}
			v, e := readValue(r)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
	case 'd':
		out := map[string]any{}
		for {
			b, e := r.readByte()
			if e != nil {
				return nil, e
			}
			if b == 'e' {
				return out, nil
			}
			if err := unread(r, b); err != nil {
				return nil, err
			}
			k, e := readValue(r)
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("dictionary key is not a string")
			}
			v, e := readValue(r)
			if e != nil {
				return nil, e
			}
			out[key] = v
		}
	default:
		raw, e := r.readUntil(':')
		if e != nil {
			return nil, e
		}
		raw = append([]byte{b}, raw...)
		n, e := strconv.Atoi(string(raw))
		if e != nil || n < 0 || n > 64*1024*1024 {
			return nil, fmt.Errorf("invalid byte string length")
		}
		data := make([]byte, n)
		if _, e = io.ReadFull(r.r, data); e != nil {
			return nil, e
		}
		return string(data), nil
	}
}

// unread is a one-byte pushback wrapper implemented by replacing the reader
// with a small prefix reader. This keeps the parser dependency-free.
func unread(r *reader, b byte) error { r.r = io.MultiReader(&singleByteReader{b: b}, r.r); return nil }

type singleByteReader struct {
	b    byte
	used bool
}

func (r *singleByteReader) Read(p []byte) (int, error) {
	if r.used || len(p) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b
	r.used = true
	return 1, nil
}

func encode(v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		return []byte(strconv.Itoa(len(x)) + ":" + x), nil
	case int64:
		return []byte("i" + strconv.FormatInt(x, 10) + "e"), nil
	case []any:
		out := []byte("l")
		for _, i := range x {
			b, e := encode(i)
			if e != nil {
				return nil, e
			}
			out = append(out, b...)
		}
		return append(out, 'e'), nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []byte("d")
		for _, k := range keys {
			kb, _ := encode(k)
			vb, e := encode(x[k])
			if e != nil {
				return nil, e
			}
			out = append(out, kb...)
			out = append(out, vb...)
		}
		return append(out, 'e'), nil
	default:
		return nil, fmt.Errorf("unsupported bencode value")
	}
}
