package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSourceRegistry(t *testing.T) {
	reg := DefaultSourceRegistry()

	// 0 is always disallowed (reserved by spec).
	if reg.Allowed(0) {
		t.Error("Allowed(0) = true; want false")
	}

	// In-range production IDs must be allowed (1–1023 per the embedded spec).
	for _, id := range []uint16{1, 2, 100, 500, 1023} {
		if !reg.Allowed(id) {
			t.Errorf("Allowed(%d) = false; want true", id)
		}
	}

	// Out-of-range IDs must be denied (1024+ is reserved/private in the spec).
	for _, id := range []uint16{1024, 1025, 32767, 32768, 65535} {
		if reg.Allowed(id) {
			t.Errorf("Allowed(%d) = true; want false", id)
		}
	}
}

func TestLoadSourceRegistry(t *testing.T) {
	// Write a temp override file with a narrow range.
	override := sourceIDsFile{
		SpecRevision: "test override",
		Ranges:       [][2]uint16{{10, 20}},
	}
	data, err := json.Marshal(override)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "src.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg, err := LoadSourceRegistry(path)
	if err != nil {
		t.Fatalf("LoadSourceRegistry: %v", err)
	}

	// 0 always denied.
	if reg.Allowed(0) {
		t.Error("Allowed(0) = true; want false")
	}
	// In override range.
	if !reg.Allowed(10) {
		t.Error("Allowed(10) = false; want true")
	}
	if !reg.Allowed(20) {
		t.Error("Allowed(20) = false; want true")
	}
	// Outside override range (but inside default production range) must be denied.
	if reg.Allowed(1) {
		t.Error("Allowed(1) = true; want false (override has range 10-20 only)")
	}
	if reg.Allowed(21) {
		t.Error("Allowed(21) = true; want false")
	}
}

func TestLoadSourceRegistry_BadPath(t *testing.T) {
	_, err := LoadSourceRegistry("/nonexistent/path/source_ids.json")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoadSourceRegistry_BadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadSourceRegistry(path)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}
