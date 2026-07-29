// internal/resources/path_gem_materializer.go
package resources

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"terraform-provider-conveyor-belt/internal/utils"
)

// pathSource is a PATH section from Gemfile.lock.
type pathSource struct {
	remote string
	gems   []pathGem
}

type pathGem struct {
	name    string
	version string
}

// materializePathGems finds PATH-sourced gems in the build-dir Gemfile.lock,
// gem-builds them on the host into vendor/cache as real .gem files, rewrites
// the build Gemfile to version pins, and re-locks. Leaves the app's real
// Gemfile untouched (only files under buildDir are mutated).
//
// Required for Lambda bare `require 'gemname'` — path installs land under
// bundler/gems/ with no specifications/, which cold-starts as LoadError.
func materializePathGems(ctx context.Context, buildDir, projectRoot string) ([]string, error) {
	gemfile := filepath.Join(buildDir, "Gemfile")
	lockfile := filepath.Join(buildDir, "Gemfile.lock")

	if _, err := os.Stat(gemfile); err != nil {
		return nil, nil
	}
	if _, err := os.Stat(lockfile); err != nil {
		return nil, nil
	}

	lockContent, err := os.ReadFile(lockfile)
	if err != nil {
		return nil, fmt.Errorf("failed to read Gemfile.lock: %w", err)
	}

	sources := parsePathSources(string(lockContent))
	if len(sources) == 0 {
		return nil, nil
	}

	cacheDir := filepath.Join(buildDir, "vendor", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create vendor/cache: %w", err)
	}

	var materialized []string
	for _, source := range sources {
		sourcePath := resolvePathRemote(source.remote, projectRoot)
		if info, err := os.Stat(sourcePath); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("path gem source missing: %s (resolved %s)", source.remote, sourcePath)
		}

		for _, gem := range source.gems {
			gemFile, err := buildPathGem(sourcePath, gem)
			if err != nil {
				return nil, err
			}
			dest := filepath.Join(cacheDir, filepath.Base(gemFile))
			if err := copyFileSimple(gemFile, dest); err != nil {
				_ = os.Remove(gemFile)
				return nil, fmt.Errorf("failed to copy built gem %s: %w", gem.name, err)
			}
			_ = os.Remove(gemFile)

			if err := rewriteGemfilePathToVersion(gemfile, gem.name, gem.version); err != nil {
				return nil, err
			}
			materialized = append(materialized, gem.name)
		}
	}

	if len(materialized) == 0 {
		return nil, nil
	}

	if err := relockPathGems(buildDir, materialized); err != nil {
		return nil, err
	}

	utils.Info(ctx, "Materialized path gems into vendor/cache", map[string]interface{}{
		"gems": materialized,
	})
	return materialized, nil
}

// parsePathSources extracts PATH remote sections and their top-level gems
// from a Gemfile.lock. Mirrors belt's PathGemMaterializer parser.
func parsePathSources(lockfileContent string) []pathSource {
	// PATH\n  remote: ...\n  specs:\n    name (version)\n      dep lines...
	sectionRe := regexp.MustCompile(`(?m)^PATH\n  remote: (.+)\n  specs:\n((?:    .+\n)*)`)
	gemRe := regexp.MustCompile(`^    (\S+)\s+\(([^)]+)\)`)

	var sources []pathSource
	matches := sectionRe.FindAllStringSubmatch(lockfileContent, -1)
	for _, match := range matches {
		remote := strings.TrimSpace(match[1])
		specsBlock := match[2]
		var gems []pathGem
		for _, line := range strings.Split(specsBlock, "\n") {
			if line == "" {
				continue
			}
			// Dependency lines are deeper-indented (6+ spaces).
			if strings.HasPrefix(line, "      ") {
				continue
			}
			if !strings.HasPrefix(line, "    ") {
				continue
			}
			gm := gemRe.FindStringSubmatch(line)
			if gm == nil {
				continue
			}
			gems = append(gems, pathGem{name: gm[1], version: gm[2]})
		}
		if len(gems) > 0 {
			sources = append(sources, pathSource{remote: remote, gems: gems})
		}
	}
	return sources
}

// parsePathRemotes returns unique remote paths from Gemfile.lock PATH sections
// (for source hashing). Relative remotes are resolved against projectRoot.
func parsePathRemotes(lockfileContent, projectRoot string) []string {
	sources := parsePathSources(lockfileContent)
	seen := make(map[string]bool)
	var remotes []string
	for _, s := range sources {
		resolved := resolvePathRemote(s.remote, projectRoot)
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		remotes = append(remotes, resolved)
	}
	return remotes
}

func resolvePathRemote(remote, projectRoot string) string {
	if strings.HasPrefix(remote, "/") {
		return remote
	}
	return filepath.Clean(filepath.Join(projectRoot, remote))
}

func buildPathGem(sourcePath string, gem pathGem) (string, error) {
	gemspec, err := findGemspec(sourcePath, gem.name)
	if err != nil {
		return "", err
	}

	// Confirm gemspec version matches lock before building.
	cmd := exec.Command("ruby", "-e",
		fmt.Sprintf("require 'rubygems'; s = Gem::Specification.load(%q); abort('load failed') unless s; puts s.version",
			filepath.Base(gemspec)))
	cmd.Dir = sourcePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to load gemspec %s: %w\n%s", gemspec, err, string(out))
	}
	specVersion := strings.TrimSpace(string(out))
	if specVersion != gem.version {
		return "", fmt.Errorf("path gem %s version mismatch: gemspec %s, lock %s", gem.name, specVersion, gem.version)
	}

	buildCmd := exec.Command("gem", "build", filepath.Base(gemspec))
	buildCmd.Dir = sourcePath
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gem build failed for %s: %w\n%s", gem.name, err, string(buildOut))
	}

	// gem build writes name-version.gem into cwd
	gemFile := filepath.Join(sourcePath, fmt.Sprintf("%s-%s.gem", gem.name, gem.version))
	if _, err := os.Stat(gemFile); err != nil {
		// Fallback: parse "File: foo.gem" from output
		fileRe := regexp.MustCompile(`(?m)File:\s+(\S+\.gem)`)
		if m := fileRe.FindStringSubmatch(string(buildOut)); m != nil {
			gemFile = filepath.Join(sourcePath, m[1])
		}
	}
	if _, err := os.Stat(gemFile); err != nil {
		return "", fmt.Errorf("built gem not found after gem build for %s (looked for %s):\n%s", gem.name, gemFile, string(buildOut))
	}
	return gemFile, nil
}

func findGemspec(sourcePath, gemName string) (string, error) {
	preferred := filepath.Join(sourcePath, gemName+".gemspec")
	if _, err := os.Stat(preferred); err == nil {
		return preferred, nil
	}
	matches, err := filepath.Glob(filepath.Join(sourcePath, "*.gemspec"))
	if err != nil {
		return "", fmt.Errorf("failed to search gemspecs in %s: %w", sourcePath, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no gemspec for path gem %s under %s", gemName, sourcePath)
	}
	return matches[0], nil
}

func rewriteGemfilePathToVersion(gemfilePath, name, version string) error {
	content, err := os.ReadFile(gemfilePath)
	if err != nil {
		return fmt.Errorf("failed to read build Gemfile: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	endsWithNewline := strings.HasSuffix(string(content), "\n")
	changed := false
	// Go RE2 has no backrefs — match single- and double-quoted gem names separately.
	nameSingle := regexp.MustCompile(fmt.Sprintf(`gem\s+'%s'`, regexp.QuoteMeta(name)))
	nameDouble := regexp.MustCompile(fmt.Sprintf(`gem\s+"%s"`, regexp.QuoteMeta(name)))
	pathRe := regexp.MustCompile(`,?\s*path:\s*['"][^'"]+['"]`)
	// Optional existing version constraint after the gem name.
	versionedSingle := regexp.MustCompile(
		fmt.Sprintf(`gem\s+'%s'(?:\s*,\s*['"][^'"]*['"])?`, regexp.QuoteMeta(name)),
	)
	versionedDouble := regexp.MustCompile(
		fmt.Sprintf(`gem\s+"%s"(?:\s*,\s*['"][^'"]*['"])?`, regexp.QuoteMeta(name)),
	)

	for i, line := range lines {
		isSingle := nameSingle.MatchString(line)
		isDouble := nameDouble.MatchString(line)
		if !isSingle && !isDouble {
			continue
		}
		if !pathRe.MatchString(line) {
			continue
		}
		cleaned := pathRe.ReplaceAllString(line, "")
		var newLine string
		if isSingle {
			newLine = versionedSingle.ReplaceAllString(cleaned, fmt.Sprintf("gem '%s', '%s'", name, version))
		} else {
			newLine = versionedDouble.ReplaceAllString(cleaned, fmt.Sprintf(`gem "%s", "%s"`, name, version))
		}
		newLine = regexp.MustCompile(`,\s*,`).ReplaceAllString(newLine, ",")
		newLine = regexp.MustCompile(`\s+,`).ReplaceAllString(newLine, ",")
		newLine = strings.TrimRight(newLine, " \t")
		if newLine != line {
			lines[i] = newLine
			changed = true
		}
	}

	if !changed {
		return fmt.Errorf("could not rewrite path: for gem %q in build Gemfile", name)
	}

	out := strings.Join(lines, "\n")
	if endsWithNewline && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(gemfilePath, []byte(out), 0644)
}

func relockPathGems(buildDir string, gemNames []string) error {
	args := append([]string{"lock", "--update"}, gemNames...)
	cmd := exec.Command("bundle", args...)
	cmd.Dir = buildDir
	// Ensure vendor/cache is visible to Bundler for unpublished versions
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to re-lock after materializing path gems (%s): %w\n%s",
			strings.Join(gemNames, ", "), err, string(out))
	}
	return nil
}

func copyFileSimple(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
