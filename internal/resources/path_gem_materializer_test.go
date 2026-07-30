package resources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePathSources(t *testing.T) {
	lock := `PATH
  remote: /tmp/fake-belt
  specs:
    belt (0.2.10)
      aws-sdk-core (~> 3)

GEM
  remote: https://rubygems.org/
  specs:
    aws-sdk-core (3.0.0)
`
	sources := parsePathSources(lock)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].remote != "/tmp/fake-belt" {
		t.Fatalf("remote: %q", sources[0].remote)
	}
	if len(sources[0].gems) != 1 || sources[0].gems[0].name != "belt" || sources[0].gems[0].version != "0.2.10" {
		t.Fatalf("gems: %+v", sources[0].gems)
	}
}

func TestRewriteGemfilePathToVersion(t *testing.T) {
	dir := t.TempDir()
	gf := filepath.Join(dir, "Gemfile")
	content := "source 'https://rubygems.org'\n\ngem 'belt', path: '/home/andy/Code/belt--discord-x'\ngem 'json'\n"
	if err := os.WriteFile(gf, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteGemfilePathToVersion(gf, "belt", "0.2.10"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(gf)
	if !strings.Contains(string(out), "gem 'belt', '0.2.10'") {
		t.Fatalf("rewrite failed:\n%s", out)
	}
	if strings.Contains(string(out), "path:") {
		t.Fatalf("path: still present:\n%s", out)
	}
}

func TestMaterializePathGemsBeltWorktree(t *testing.T) {
	// Use the real belt worktree if present
	beltPath := "/home/andy/Code/belt--discord-galen-59870339"
	if _, err := os.Stat(filepath.Join(beltPath, "belt.gemspec")); err != nil {
		t.Skip("belt worktree not available")
	}
	// Read version from gemspec
	// build a temp project
	project := t.TempDir()
	// get version
	// Use known 0.2.10 from branch
	version := "0.2.10"
	gemfile := "source 'https://rubygems.org'\n\ngem 'belt', path: '" + beltPath + "'\n"
	if err := os.WriteFile(filepath.Join(project, "Gemfile"), []byte(gemfile), 0644); err != nil {
		t.Fatal(err)
	}
	// Need a real lockfile with PATH section - generate with bundle lock
	// Or write a minimal one matching the gemspec version
	// Better: run bundle lock in project
	// Write a lockfile manually for speed
	lock := "PATH\n  remote: " + beltPath + "\n  specs:\n    belt (" + version + ")\n\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  belt!\n\nBUNDLED WITH\n   2.5.0\n"
	if err := os.WriteFile(filepath.Join(project, "Gemfile.lock"), []byte(lock), 0644); err != nil {
		t.Fatal(err)
	}

	buildDir := t.TempDir()
	// copy gemfile/lock to build
	os.WriteFile(filepath.Join(buildDir, "Gemfile"), []byte(gemfile), 0644)
	os.WriteFile(filepath.Join(buildDir, "Gemfile.lock"), []byte(lock), 0644)

	gems, err := materializePathGems(context.Background(), buildDir, project)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(gems) != 1 || gems[0] != "belt" {
		t.Fatalf("gems: %v", gems)
	}
	gemFile := filepath.Join(buildDir, "vendor", "cache", "belt-"+version+".gem")
	if _, err := os.Stat(gemFile); err != nil {
		t.Fatalf("expected %s: %v", gemFile, err)
	}
	gfOut, _ := os.ReadFile(filepath.Join(buildDir, "Gemfile"))
	if strings.Contains(string(gfOut), "path:") {
		t.Fatalf("build Gemfile still has path:\n%s", gfOut)
	}
	lockOut, _ := os.ReadFile(filepath.Join(buildDir, "Gemfile.lock"))
	if strings.Contains(string(lockOut), "PATH\n") {
		t.Fatalf("build lock still has PATH:\n%s", lockOut)
	}
	// Real project Gemfile untouched
	// (we didn't write a "real" one outside buildDir with path - project has path; build should be rewritten only)
	projGF, _ := os.ReadFile(filepath.Join(project, "Gemfile"))
	if !strings.Contains(string(projGF), "path:") {
		t.Fatal("project Gemfile should still have path:")
	}
}
