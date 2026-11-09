package resources

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// pathGemSkipDirs are directory names that never contribute to a PATH gem's
// runtime behaviour and churn constantly (agent logs in tmp/, test runs in
// coverage/, installed bundles in vendor/, ...). They are skipped at any depth
// when hashing a PATH remote, in addition to whatever the remote's own
// .gitignore excludes. Dot-directories (.git, .bundle, .conveyor-belt) are
// skipped by hashDirectoryContents itself.
var pathGemSkipDirs = map[string]bool{
	"tmp":          true,
	"log":          true,
	"logs":         true,
	"node_modules": true,
	"vendor":       true,
	"coverage":     true,
	"spec":         true,
	"test":         true,
	"tests":        true,
	"features":     true,
	"pkg":          true,
	"doc":          true,
	"docs":         true,
}

// gitignoreRule is one parsed line of a .gitignore file.
type gitignoreRule struct {
	negate   bool
	dirOnly  bool
	anchored bool
	re       *regexp.Regexp
}

// gitignoreMatcher evaluates a single .gitignore file (the one at the root of
// the directory being hashed). Nested .gitignore files are not consulted; the
// root file plus pathGemSkipDirs covers the realistic cases for a gem checkout.
type gitignoreMatcher struct {
	rules []gitignoreRule
}

// loadGitignore parses <dir>/.gitignore. A missing or unreadable file yields
// an empty matcher (nothing ignored).
func loadGitignore(dir string) *gitignoreMatcher {
	m := &gitignoreMatcher{}
	f, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return m
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if rule, ok := parseGitignoreLine(scanner.Text()); ok {
			m.rules = append(m.rules, rule)
		}
	}
	return m
}

// parseGitignoreLine converts one .gitignore pattern into a rule. Supports the
// common subset: comments, negation (!), directory-only (trailing /), anchoring
// (leading / or an inner /), and the *, ?, ** wildcards.
func parseGitignoreLine(line string) (gitignoreRule, bool) {
	// Trailing spaces are ignored unless escaped; we don't support escapes.
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return gitignoreRule{}, false
	}

	rule := gitignoreRule{}
	if strings.HasPrefix(line, "!") {
		rule.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		rule.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		rule.anchored = true
		line = strings.TrimPrefix(line, "/")
	} else if strings.HasPrefix(line, "**/") {
		line = strings.TrimPrefix(line, "**/")
	} else if strings.Contains(line, "/") {
		// A slash anywhere else anchors the pattern to the .gitignore's directory.
		rule.anchored = true
	}
	if line == "" {
		return gitignoreRule{}, false
	}

	rule.re = gitignorePatternToRegexp(line)
	return rule, true
}

// gitignorePatternToRegexp translates a gitignore glob into an anchored regexp
// over a slash-separated relative path (or a bare basename for unanchored rules).
func gitignorePatternToRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// "**" — any number of path segments
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// ignored reports whether relPath (slash-separated, relative to the .gitignore
// directory) is excluded. Later rules win, so a negation can re-include a path
// that an earlier rule excluded.
func (m *gitignoreMatcher) ignored(relPath string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	base := relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		base = relPath[idx+1:]
	}

	result := false
	for _, rule := range m.rules {
		if rule.dirOnly && !isDir {
			continue
		}
		subject := base
		if rule.anchored {
			subject = relPath
		}
		if rule.re.MatchString(subject) {
			result = !rule.negate
		}
	}
	return result
}

// hashPathGemContents hashes a Gemfile.lock PATH remote (a sibling gem checkout
// or a monorepo root) the way a gem build would see it: dot-directories, the
// pathGemSkipDirs set, and anything the remote's root .gitignore excludes are
// left out. This keeps the hash stable across `.git/` writes, agent logs in
// `tmp/`, test coverage output, and the provider's own `.conveyor-belt/` spec
// dumps, so a clean tree plans "No changes".
func hashPathGemContents(dirPath string) (string, error) {
	ignore := loadGitignore(dirPath)
	hasher := sha256.New()

	var files []string
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dirPath {
			return nil
		}

		name := info.Name()
		rel, relErr := filepath.Rel(dirPath, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if info.IsDir() {
			if strings.HasPrefix(name, ".") || pathGemSkipDirs[name] || ignore.ignored(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(name, ".") ||
			strings.HasSuffix(name, ".swp") ||
			strings.HasSuffix(name, ".swo") ||
			strings.HasSuffix(name, "~") ||
			strings.HasSuffix(name, ".gem") || // build artifacts from `gem build`, never source
			ignore.ignored(rel, false) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(files)
	for _, filePath := range files {
		relPath, err := filepath.Rel(dirPath, filePath)
		if err != nil {
			return "", err
		}
		hasher.Write([]byte(filepath.ToSlash(relPath)))
		hasher.Write([]byte{0})
		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", err
		}
		hasher.Write(content)
		hasher.Write([]byte{0})
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
