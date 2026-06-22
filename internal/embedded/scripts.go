// Package embedded provides functions to extract and manage the Ruby DSL scripts
// that are embedded in the provider binary at compile time.
package embedded

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	embeddedscripts "terraform-provider-conveyor-belt"
)

// ExtractScripts extracts the embedded Ruby scripts to a temp directory.
// The directory name includes a content hash for idempotent extraction —
// if the same scripts have already been extracted, the existing directory is reused.
// Returns the path to the extracted directory.
func ExtractScripts() (string, error) {
	hash, err := contentHash()
	if err != nil {
		return "", fmt.Errorf("computing embedded scripts hash: %w", err)
	}

	dir := filepath.Join(os.TempDir(), fmt.Sprintf("dispatcher-scripts-%s", hash[:12]))

	// Idempotent: reuse existing extraction only if the directory AND key files are intact.
	// macOS periodically purges temp file contents while leaving directories behind,
	// which causes "No such file or directory" errors when Ruby tries to load the script.
	if isExtractionIntact(dir) {
		return dir, nil
	}

	// Remove stale/partial directory before re-extracting
	os.RemoveAll(dir)

	if err := extractAll(dir); err != nil {
		// Clean up partial extraction on failure
		os.RemoveAll(dir)
		return "", fmt.Errorf("extracting embedded scripts to %s: %w", dir, err)
	}

	return dir, nil
}

// Cleanup removes the extracted scripts directory.
func Cleanup(dir string) error {
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

// ScriptPath returns the full path to a specific script within the extracted directory.
func ScriptPath(extractedDir, scriptName string) string {
	if scriptName == "" {
		return filepath.Join(extractedDir, "scripts")
	}
	return filepath.Join(extractedDir, "scripts", scriptName)
}

// isExtractionIntact checks that the extracted directory exists and contains
// the key script files. Returns false if the directory is missing, empty,
// or has been partially cleaned up by the OS.
func isExtractionIntact(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	// Spot-check that the main script exists
	if _, err := os.Stat(filepath.Join(dir, "scripts", "list_routes.rb")); err != nil {
		return false
	}
	return true
}

// contentHash computes a SHA256 hash of all embedded file contents.
// Files are processed in sorted order for deterministic hashing.
func contentHash() (string, error) {
	h := sha256.New()

	var paths []string
	err := fs.WalkDir(embeddedscripts.Scripts, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(paths)

	for _, path := range paths {
		data, err := fs.ReadFile(embeddedscripts.Scripts, path)
		if err != nil {
			return "", err
		}
		h.Write([]byte(path))
		h.Write(data)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// WriteScriptsTo copies the embedded Ruby scripts to the given directory.
// Unlike ExtractScripts (which uses a temp dir), this writes to a stable,
// user-visible location so external tools can reference the scripts reliably.
func WriteScriptsTo(dir string) error {
	return extractAll(dir)
}

// extractAll writes all embedded files to the target directory, preserving
// the directory structure.
func extractAll(dir string) error {
	return fs.WalkDir(embeddedscripts.Scripts, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		target := filepath.Join(dir, path)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := fs.ReadFile(embeddedscripts.Scripts, path)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})
}
