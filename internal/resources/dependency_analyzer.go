// internal/resources/dependency_analyzer.go
package resources

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"terraform-provider-conveyor-belt/internal/utils"
)

// DependencyAnalyzer analyzes Ruby require statements to determine which shared files
// each Lambda uses, enabling smarter change detection.
type DependencyAnalyzer struct {
	sourceDir  string
	sharedDirs []string

	// Cache for dependency analysis results
	cache      map[string]*dependencyEntry
	cacheMu    sync.RWMutex

	// Track file modification times for cache invalidation
	modTimes   map[string]time.Time
	modTimesMu sync.RWMutex
}

// dependencyEntry holds cached dependency information for a Lambda
type dependencyEntry struct {
	dependencies []string
	analyzedAt   time.Time
}

// DependencyAnalyzerOption is a functional option for configuring DependencyAnalyzer
type DependencyAnalyzerOption func(*DependencyAnalyzer)

// WithSharedDirectories sets the shared directories to analyze
func WithSharedDirectories(dirs []string) DependencyAnalyzerOption {
	return func(da *DependencyAnalyzer) {
		da.sharedDirs = dirs
	}
}

// NewDependencyAnalyzer creates a new DependencyAnalyzer for the given source directory
func NewDependencyAnalyzer(sourceDir string, opts ...DependencyAnalyzerOption) *DependencyAnalyzer {
	da := &DependencyAnalyzer{
		sourceDir:  sourceDir,
		sharedDirs: []string{"models", "lib", "helpers", "templates"},
		cache:      make(map[string]*dependencyEntry),
		modTimes:   make(map[string]time.Time),
	}

	for _, opt := range opts {
		opt(da)
	}

	return da
}

// Regex patterns for Ruby require statements
// Matches: require 'file', require "file", require_relative 'file', require_relative "file"
var (
	requireRegex         = regexp.MustCompile(`require\s+['"]([^'"]+)['"]`)
	requireRelativeRegex = regexp.MustCompile(`require_relative\s+['"]([^'"]+)['"]`)
)

// AnalyzeDependencies parses Ruby require statements to find all dependencies for a Lambda.
// Returns a list of absolute file paths that the Lambda depends on.
func (da *DependencyAnalyzer) AnalyzeDependencies(ctx context.Context, lambdaName string) ([]string, error) {
	// Check cache first
	da.cacheMu.RLock()
	entry, exists := da.cache[lambdaName]
	da.cacheMu.RUnlock()

	if exists {
		// Check if cache is still valid (source file hasn't changed)
		rubyFile := filepath.Join(da.sourceDir, lambdaName+".rb")
		if da.isCacheValid(rubyFile, entry.analyzedAt) {
			utils.Debug(ctx, "Using cached dependency analysis", map[string]interface{}{
				"lambda": lambdaName,
			})
			return entry.dependencies, nil
		}
	}

	// Perform fresh analysis
	rubyFile := filepath.Join(da.sourceDir, lambdaName+".rb")
	absRubyFile, err := filepath.Abs(rubyFile)
	if err != nil {
		return nil, err
	}

	// Check if the Ruby file exists
	if _, err := os.Stat(absRubyFile); os.IsNotExist(err) {
		// Lambda file doesn't exist - return empty dependencies
		utils.Debug(ctx, "Lambda source file not found", map[string]interface{}{
			"lambda": lambdaName,
			"path":   absRubyFile,
		})
		return []string{}, nil
	}

	// Analyze dependencies recursively with cycle detection
	visited := make(map[string]bool)
	deps, err := da.analyzeDependenciesRecursive(ctx, absRubyFile, visited)
	if err != nil {
		return nil, err
	}

	// Include the main file itself
	allDeps := append([]string{absRubyFile}, deps...)

	// Remove duplicates and sort for determinism
	allDeps = uniqueStrings(allDeps)
	sort.Strings(allDeps)

	// Update cache
	da.cacheMu.Lock()
	da.cache[lambdaName] = &dependencyEntry{
		dependencies: allDeps,
		analyzedAt:   time.Now(),
	}
	da.cacheMu.Unlock()

	// Update mod time tracking
	da.updateModTime(absRubyFile)

	utils.Debug(ctx, "Completed dependency analysis", map[string]interface{}{
		"lambda":     lambdaName,
		"dep_count":  len(allDeps),
	})

	return allDeps, nil
}

// analyzeDependenciesRecursive recursively analyzes dependencies with cycle detection
func (da *DependencyAnalyzer) analyzeDependenciesRecursive(ctx context.Context, filePath string, visited map[string]bool) ([]string, error) {
	// Normalize path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}

	// Check for cycles
	if visited[absPath] {
		utils.Debug(ctx, "Cycle detected in dependency analysis", map[string]interface{}{
			"file": absPath,
		})
		return []string{}, nil
	}
	visited[absPath] = true

	// Read file content
	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - skip it
			return []string{}, nil
		}
		return nil, err
	}

	var deps []string

	// Parse require statements
	requireMatches := requireRegex.FindAllStringSubmatch(string(content), -1)
	for _, match := range requireMatches {
		if len(match) > 1 {
			requiredFile := match[1]
			resolvedPath := da.resolveRequire(requiredFile, absPath)
			if resolvedPath != "" {
				deps = append(deps, resolvedPath)
				// Recursively analyze
				subDeps, err := da.analyzeDependenciesRecursive(ctx, resolvedPath, visited)
				if err != nil {
					utils.Warn(ctx, "Failed to analyze sub-dependency", map[string]interface{}{
						"file":  resolvedPath,
						"error": err.Error(),
					})
					// Continue with other dependencies
				} else {
					deps = append(deps, subDeps...)
				}
			}
		}
	}

	// Parse require_relative statements
	requireRelativeMatches := requireRelativeRegex.FindAllStringSubmatch(string(content), -1)
	for _, match := range requireRelativeMatches {
		if len(match) > 1 {
			requiredFile := match[1]
			resolvedPath := da.resolveRequireRelative(requiredFile, absPath)
			if resolvedPath != "" {
				deps = append(deps, resolvedPath)
				// Recursively analyze
				subDeps, err := da.analyzeDependenciesRecursive(ctx, resolvedPath, visited)
				if err != nil {
					utils.Warn(ctx, "Failed to analyze sub-dependency", map[string]interface{}{
						"file":  resolvedPath,
						"error": err.Error(),
					})
					// Continue with other dependencies
				} else {
					deps = append(deps, subDeps...)
				}
			}
		}
	}

	return deps, nil
}

// resolveRequire resolves a require statement to an absolute file path.
// It searches in the source directory and shared directories.
func (da *DependencyAnalyzer) resolveRequire(requiredFile string, fromFile string) string {
	// Add .rb extension if not present
	if !strings.HasSuffix(requiredFile, ".rb") {
		requiredFile = requiredFile + ".rb"
	}

	// Get absolute source directory
	absSourceDir, err := filepath.Abs(da.sourceDir)
	if err != nil {
		return ""
	}

	// Try to find in source directory root
	candidate := filepath.Join(absSourceDir, requiredFile)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	// Try to find in shared directories
	for _, sharedDir := range da.sharedDirs {
		candidate := filepath.Join(absSourceDir, sharedDir, requiredFile)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Try relative to the requiring file's directory
	fromDir := filepath.Dir(fromFile)
	candidate = filepath.Join(fromDir, requiredFile)
	if _, err := os.Stat(candidate); err == nil {
		absCandidate, err := filepath.Abs(candidate)
		if err == nil {
			return absCandidate
		}
	}

	// Not found in project - likely a gem or stdlib
	return ""
}

// resolveRequireRelative resolves a require_relative statement to an absolute file path.
// require_relative is always relative to the file containing the statement.
func (da *DependencyAnalyzer) resolveRequireRelative(requiredFile string, fromFile string) string {
	// Add .rb extension if not present
	if !strings.HasSuffix(requiredFile, ".rb") {
		requiredFile = requiredFile + ".rb"
	}

	// require_relative is relative to the directory of the file containing the statement
	fromDir := filepath.Dir(fromFile)
	candidate := filepath.Join(fromDir, requiredFile)

	// Normalize the path
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return ""
	}

	// Check if file exists
	if _, err := os.Stat(absCandidate); err == nil {
		return absCandidate
	}

	return ""
}

// isCacheValid checks if the cached analysis is still valid
func (da *DependencyAnalyzer) isCacheValid(filePath string, analyzedAt time.Time) bool {
	da.modTimesMu.RLock()
	cachedModTime, exists := da.modTimes[filePath]
	da.modTimesMu.RUnlock()

	if !exists {
		return false
	}

	// Get current mod time
	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	currentModTime := info.ModTime()

	// Cache is valid if file hasn't been modified since we cached it
	return !currentModTime.After(cachedModTime)
}

// updateModTime updates the tracked modification time for a file
func (da *DependencyAnalyzer) updateModTime(filePath string) {
	info, err := os.Stat(filePath)
	if err != nil {
		return
	}

	da.modTimesMu.Lock()
	da.modTimes[filePath] = info.ModTime()
	da.modTimesMu.Unlock()
}

// InvalidateCache invalidates the cache for a specific Lambda
func (da *DependencyAnalyzer) InvalidateCache(lambdaName string) {
	da.cacheMu.Lock()
	delete(da.cache, lambdaName)
	da.cacheMu.Unlock()
}

// InvalidateAllCache clears the entire cache
func (da *DependencyAnalyzer) InvalidateAllCache() {
	da.cacheMu.Lock()
	da.cache = make(map[string]*dependencyEntry)
	da.cacheMu.Unlock()

	da.modTimesMu.Lock()
	da.modTimes = make(map[string]time.Time)
	da.modTimesMu.Unlock()
}

// uniqueStrings removes duplicates from a string slice
func uniqueStrings(input []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(input))

	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return result
}


// GetAffectedLambdas returns a list of Lambda names that are affected by changes to the given files.
// This is the main entry point for smart change detection.
func (da *DependencyAnalyzer) GetAffectedLambdas(ctx context.Context, changedFiles []string) ([]string, error) {
	utils.Info(ctx, "Analyzing affected Lambdas for changed files", map[string]interface{}{
		"changed_files_count": len(changedFiles),
	})

	// Discover all lambdas in the source directory
	lambdas, err := da.discoverLambdas()
	if err != nil {
		utils.Warn(ctx, "Failed to discover Lambdas, falling back to marking all as affected", map[string]interface{}{
			"error": err.Error(),
		})
		return da.fallbackAllLambdas(ctx)
	}

	if len(lambdas) == 0 {
		return []string{}, nil
	}

	// Normalize changed files to absolute paths
	normalizedChangedFiles := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		absPath, err := filepath.Abs(f)
		if err != nil {
			normalizedChangedFiles = append(normalizedChangedFiles, f)
		} else {
			normalizedChangedFiles = append(normalizedChangedFiles, absPath)
		}
	}

	affected := make(map[string]bool)

	for _, lambda := range lambdas {
		deps, err := da.AnalyzeDependencies(ctx, lambda)
		if err != nil {
			// If analysis fails for this lambda, mark it as affected (conservative)
			utils.Warn(ctx, "Dependency analysis failed for Lambda, marking as affected", map[string]interface{}{
				"lambda": lambda,
				"error":  err.Error(),
			})
			affected[lambda] = true
			continue
		}

		// Check if any changed file is in this lambda's dependencies
		for _, changedFile := range normalizedChangedFiles {
			for _, dep := range deps {
				if da.pathsMatch(dep, changedFile) {
					affected[lambda] = true
					utils.Debug(ctx, "Lambda affected by changed file", map[string]interface{}{
						"lambda":       lambda,
						"changed_file": changedFile,
						"dependency":   dep,
					})
					break
				}
			}
			if affected[lambda] {
				break
			}
		}
	}

	// Convert map to sorted slice
	result := make([]string, 0, len(affected))
	for lambda := range affected {
		result = append(result, lambda)
	}
	sort.Strings(result)

	utils.Info(ctx, "Completed affected Lambda analysis", map[string]interface{}{
		"total_lambdas":    len(lambdas),
		"affected_lambdas": len(result),
	})

	return result, nil
}

// pathsMatch checks if two paths refer to the same file
func (da *DependencyAnalyzer) pathsMatch(path1, path2 string) bool {
	// Direct match
	if path1 == path2 {
		return true
	}

	// Try to resolve to absolute paths and compare
	abs1, err1 := filepath.Abs(path1)
	abs2, err2 := filepath.Abs(path2)
	if err1 == nil && err2 == nil && abs1 == abs2 {
		return true
	}

	// Check if one path ends with the other (for relative path matching)
	if strings.HasSuffix(path1, path2) || strings.HasSuffix(path2, path1) {
		return true
	}

	// Check base names match and are in same directory structure
	base1 := filepath.Base(path1)
	base2 := filepath.Base(path2)
	if base1 == base2 {
		// Additional check: ensure they're likely the same file
		// by checking parent directory names
		dir1 := filepath.Base(filepath.Dir(path1))
		dir2 := filepath.Base(filepath.Dir(path2))
		if dir1 == dir2 {
			return true
		}
	}

	return false
}

// discoverLambdas scans the source directory for all .rb files (Lambda handlers)
func (da *DependencyAnalyzer) discoverLambdas() ([]string, error) {
	absSourceDir, err := filepath.Abs(da.sourceDir)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(absSourceDir)
	if err != nil {
		return nil, err
	}

	var lambdas []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".rb") {
			// Extract lambda name (filename without .rb extension)
			lambdaName := strings.TrimSuffix(name, ".rb")
			lambdas = append(lambdas, lambdaName)
		}
	}

	sort.Strings(lambdas)
	return lambdas, nil
}

// fallbackAllLambdas returns all lambdas when dependency analysis fails.
// This is the conservative fallback behavior per Requirement 5.3.
func (da *DependencyAnalyzer) fallbackAllLambdas(ctx context.Context) ([]string, error) {
	utils.Warn(ctx, "Falling back to marking all Lambdas as affected", nil)

	lambdas, err := da.discoverLambdas()
	if err != nil {
		return nil, err
	}

	return lambdas, nil
}

// CheckCacheValidity checks if the cache for a Lambda is still valid based on file modification times.
// This implements cache invalidation per Requirement 5.4.
func (da *DependencyAnalyzer) CheckCacheValidity(ctx context.Context, lambdaName string) bool {
	da.cacheMu.RLock()
	entry, exists := da.cache[lambdaName]
	da.cacheMu.RUnlock()

	if !exists {
		return false
	}

	// Check the main Lambda file
	rubyFile := filepath.Join(da.sourceDir, lambdaName+".rb")
	absRubyFile, err := filepath.Abs(rubyFile)
	if err != nil {
		return false
	}

	// Check if main file has been modified
	info, err := os.Stat(absRubyFile)
	if err != nil {
		return false
	}

	if info.ModTime().After(entry.analyzedAt) {
		utils.Debug(ctx, "Cache invalidated: source file modified", map[string]interface{}{
			"lambda":      lambdaName,
			"file":        absRubyFile,
			"analyzed_at": entry.analyzedAt,
			"modified_at": info.ModTime(),
		})
		return false
	}

	// Check all dependencies for modifications
	for _, dep := range entry.dependencies {
		depInfo, err := os.Stat(dep)
		if err != nil {
			// File no longer exists - cache is invalid
			return false
		}

		if depInfo.ModTime().After(entry.analyzedAt) {
			utils.Debug(ctx, "Cache invalidated: dependency modified", map[string]interface{}{
				"lambda":      lambdaName,
				"dependency":  dep,
				"analyzed_at": entry.analyzedAt,
				"modified_at": depInfo.ModTime(),
			})
			return false
		}
	}

	return true
}

// InvalidateCacheIfStale checks and invalidates cache for a Lambda if any of its files have changed.
// Returns true if cache was invalidated.
func (da *DependencyAnalyzer) InvalidateCacheIfStale(ctx context.Context, lambdaName string) bool {
	if !da.CheckCacheValidity(ctx, lambdaName) {
		da.InvalidateCache(lambdaName)
		return true
	}
	return false
}

// GetCachedDependencies returns cached dependencies for a Lambda without re-analyzing.
// Returns nil if not cached.
func (da *DependencyAnalyzer) GetCachedDependencies(lambdaName string) []string {
	da.cacheMu.RLock()
	defer da.cacheMu.RUnlock()

	entry, exists := da.cache[lambdaName]
	if !exists {
		return nil
	}

	// Return a copy to prevent external modification
	result := make([]string, len(entry.dependencies))
	copy(result, entry.dependencies)
	return result
}

// SourceDir returns the source directory being analyzed
func (da *DependencyAnalyzer) SourceDir() string {
	return da.sourceDir
}

// SharedDirs returns the shared directories being analyzed
func (da *DependencyAnalyzer) SharedDirs() []string {
	return da.sharedDirs
}
