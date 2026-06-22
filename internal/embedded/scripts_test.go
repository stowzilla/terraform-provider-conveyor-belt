package embedded

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractScripts_ReExtractsWhenFilesAreMissing(t *testing.T) {
	// First extraction — should succeed
	dir, err := ExtractScripts()
	if err != nil {
		t.Fatalf("initial extraction failed: %v", err)
	}
	defer Cleanup(dir)

	scriptPath := filepath.Join(dir, "scripts", "list_routes.rb")

	// Verify the script exists
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("list_routes.rb not found after extraction: %v", err)
	}

	// Simulate macOS temp cleanup: remove the script file but leave the directory
	if err := os.Remove(scriptPath); err != nil {
		t.Fatalf("failed to remove script for test: %v", err)
	}

	// Second extraction — should detect the missing file and re-extract
	dir2, err := ExtractScripts()
	if err != nil {
		t.Fatalf("re-extraction failed: %v", err)
	}

	if dir2 != dir {
		t.Errorf("expected same directory path, got %s vs %s", dir, dir2)
	}

	// Verify the script was restored
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("list_routes.rb not restored after re-extraction: %v", err)
	}
}

func TestIsExtractionIntact_FalseForMissingDir(t *testing.T) {
	if isExtractionIntact("/nonexistent/path") {
		t.Error("expected false for nonexistent directory")
	}
}

func TestIsExtractionIntact_FalseForEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if isExtractionIntact(dir) {
		t.Error("expected false for empty directory")
	}
}
