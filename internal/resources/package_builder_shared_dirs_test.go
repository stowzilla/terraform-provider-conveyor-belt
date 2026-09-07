// internal/resources/package_builder_shared_dirs_test.go
package resources

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Regression coverage for the "cannot load such file -- /var/task/config/environment"
// Lambda init crash (feature_parity PR #89).
//
// Root cause: the package builder only copies directories listed in
// `lambda_shared_dirs` into the deployment zip. `config/` was missing from that
// list, so `config/environment.rb` (require_relative'd at boot by api.rb and
// pay_webhooks.rb) never made it into /var/task, and the Lambda died at init.
//
// These tests exercise the real copy-shared-dirs + zip path (no Docker, no mocks)
// and prove that every directory named in sharedDirs — including `config` — lands
// in the zip with its file contents intact.

// zipEntriesSharedDirsTest reads a zip archive and returns a map of relative file
// path -> file contents.
func zipEntriesSharedDirsTest(t *testing.T, zipData []byte) map[string]string {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}

	entries := make(map[string]string)
	for _, f := range reader.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("failed to open zip entry %q: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			rc.Close()
			t.Fatalf("failed to read zip entry %q: %v", f.Name, err)
		}
		rc.Close()
		entries[filepath.ToSlash(f.Name)] = buf.String()
	}
	return entries
}

// buildSharedDirZip mirrors the shared-directory copy loop from buildSinglePackage:
// for each dir in pb.sharedDirs that exists under sourceDir, copy it into a build
// dir, then zip the build dir. It returns the resulting zip bytes.
//
// It deliberately uses the SAME production helpers (copyDirectory, createZipFromDirectory)
// and the SAME existence-check + skip semantics as buildSinglePackage, so the copy
// contract is exercised for real without requiring Docker to bundle gems.
func buildSharedDirZip(t *testing.T, pb *PackageBuilder, entryRuby string) []byte {
	t.Helper()

	buildDir := t.TempDir()

	// Entry .rb file (as buildSinglePackage would copy it in).
	if entryRuby != "" {
		if err := os.WriteFile(filepath.Join(buildDir, "api.rb"), []byte(entryRuby), 0644); err != nil {
			t.Fatalf("failed to write entry ruby: %v", err)
		}
	}

	for _, dirName := range pb.sharedDirs {
		src := filepath.Join(pb.sourceDir, dirName)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			if err := pb.copyDirectory(src, filepath.Join(buildDir, dirName)); err != nil {
				t.Fatalf("copyDirectory(%q) failed: %v", dirName, err)
			}
		}
	}

	zipData, err := pb.createZipFromDirectory(buildDir)
	if err != nil {
		t.Fatalf("createZipFromDirectory failed: %v", err)
	}
	return zipData
}

// writeSourceTreeSharedDirsTest creates a fake lambda source dir with the given files.
// Keys are relative paths (using "/" separators), values are file contents.
func writeSourceTreeSharedDirsTest(t *testing.T, files map[string]string) string {
	t.Helper()

	sourceDir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(sourceDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %q failed: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %q failed: %v", rel, err)
		}
	}
	return sourceDir
}

// fp:package_config_dir_in_zip@api
// TestSharedDirs_ConfigDirIsPackaged is the direct regression test for PR #89:
// when "config" is listed in sharedDirs, config/environment.rb must land in the zip.
func TestSharedDirs_ConfigDirIsPackaged(t *testing.T) {
	sourceDir := writeSourceTreeSharedDirsTest(t, map[string]string{
		"config/environment.rb":                "# boot the app\nputs 'env loaded'\n",
		"config/plans.rb":                      "# billing plans\n",
		"models/post.rb":                       "class Post; end\n",
		"controllers/api/things_controller.rb": "class ThingsController; end\n",
	})

	pb := NewPackageBuilder(
		sourceDir,
		// This is the fixed list from infrastructure/modules/app/main.tf.
		WithSharedDirs([]string{"config", "controllers", "helpers", "lib", "models", "views"}),
	)

	entries := zipEntriesSharedDirsTest(t, buildSharedDirZip(t, pb, "require_relative 'config/environment'\n"))

	// The exact file whose absence caused the init crash.
	if _, ok := entries["config/environment.rb"]; !ok {
		t.Fatalf("config/environment.rb missing from zip; entries: %v", keysSharedDirsTest(entries))
	}
	if got := entries["config/environment.rb"]; got != "# boot the app\nputs 'env loaded'\n" {
		t.Errorf("config/environment.rb content mismatch: %q", got)
	}

	// The sibling file that would have taken down pay_webhooks.rb next.
	if _, ok := entries["config/plans.rb"]; !ok {
		t.Errorf("config/plans.rb missing from zip; entries: %v", keysSharedDirsTest(entries))
	}

	// Sanity: the other shared dirs still make it in.
	if _, ok := entries["models/post.rb"]; !ok {
		t.Errorf("models/post.rb missing from zip; entries: %v", keysSharedDirsTest(entries))
	}
	if _, ok := entries["controllers/api/things_controller.rb"]; !ok {
		t.Errorf("nested controllers file missing from zip; entries: %v", keysSharedDirsTest(entries))
	}
}

// fp:package_config_dir_in_zip
// TestSharedDirs_ConfigOmitted_ReproducesInitCrash locks in the failure mode:
// with the OLD sharedDirs list (no "config"), config/environment.rb is NOT in the
// zip — which is precisely what produced the LoadError at Lambda init. If someone
// ever drops "config" from the list again, this test turns red.
func TestSharedDirs_ConfigOmitted_ReproducesInitCrash(t *testing.T) {
	sourceDir := writeSourceTreeSharedDirsTest(t, map[string]string{
		"config/environment.rb": "# boot the app\n",
		"models/post.rb":        "class Post; end\n",
	})

	pb := NewPackageBuilder(
		sourceDir,
		// The BROKEN list, before PR #89.
		WithSharedDirs([]string{"controllers", "helpers", "lib", "models", "views"}),
	)

	entries := zipEntriesSharedDirsTest(t, buildSharedDirZip(t, pb, "require_relative 'config/environment'\n"))

	if _, ok := entries["config/environment.rb"]; ok {
		t.Fatalf("expected config/environment.rb to be ABSENT with the old sharedDirs list, "+
			"but it was present; entries: %v", keysSharedDirsTest(entries))
	}
	// models still packaged, proving the omission is specific to config/.
	if _, ok := entries["models/post.rb"]; !ok {
		t.Errorf("models/post.rb should still be packaged; entries: %v", keysSharedDirsTest(entries))
	}
}

// fp:package_config_dir_in_zip
// TestSharedDirs_MissingDirIsSkippedGracefully ensures listing a shared dir that
// doesn't exist on disk doesn't blow up the build — it's simply skipped.
func TestSharedDirs_MissingDirIsSkippedGracefully(t *testing.T) {
	sourceDir := writeSourceTreeSharedDirsTest(t, map[string]string{
		"config/environment.rb": "# boot\n",
	})

	pb := NewPackageBuilder(
		sourceDir,
		// "views" and "helpers" don't exist in this tree.
		WithSharedDirs([]string{"config", "controllers", "helpers", "lib", "models", "views"}),
	)

	entries := zipEntriesSharedDirsTest(t, buildSharedDirZip(t, pb, ""))

	if _, ok := entries["config/environment.rb"]; !ok {
		t.Fatalf("config/environment.rb missing; entries: %v", keysSharedDirsTest(entries))
	}
	// No panic, no phantom entries for the absent dirs.
	for name := range entries {
		if name == "views" || name == "helpers" {
			t.Errorf("unexpected entry for non-existent dir: %q", name)
		}
	}
}

// fp:package_config_dir_in_zip
// TestSharedDirs_NestedFilesPreserveStructure verifies deep directory trees inside
// a shared dir keep their relative paths in the zip (so require_relative works at
// runtime for nested files like config/initializers/*.rb).
func TestSharedDirs_NestedFilesPreserveStructure(t *testing.T) {
	sourceDir := writeSourceTreeSharedDirsTest(t, map[string]string{
		"config/environment.rb":      "# boot\n",
		"config/initializers/pay.rb": "# belt-pay config\n",
		"lib/routes/api_routes.rb":   "module Routes; API = []; end\n",
	})

	pb := NewPackageBuilder(
		sourceDir,
		WithSharedDirs([]string{"config", "lib"}),
	)

	entries := zipEntriesSharedDirsTest(t, buildSharedDirZip(t, pb, ""))

	want := []string{
		"config/environment.rb",
		"config/initializers/pay.rb",
		"lib/routes/api_routes.rb",
	}
	for _, w := range want {
		if _, ok := entries[w]; !ok {
			t.Errorf("expected nested entry %q in zip; entries: %v", w, keysSharedDirsTest(entries))
		}
	}
}

// keysSharedDirsTest returns the key list of a map for readable failure output.
func keysSharedDirsTest(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
