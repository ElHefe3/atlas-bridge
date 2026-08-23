package acquisition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompletedFileIsConsumedAndRemoved(t *testing.T) {
	m := NewManager()
	path := filepath.Join(t.TempDir(), "completed.part")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.files["job"] = path
	file, ok := m.File("job")
	if !ok {
		t.Fatal("completed file was not returned")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed file was not removed: %v", err)
	}
	if _, ok = m.File("job"); ok {
		t.Fatal("completed file was consumable more than once")
	}
}
