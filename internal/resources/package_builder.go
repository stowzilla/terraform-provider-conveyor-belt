// internal/resources/package_builder.go
package resources

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"terraform-provider-conveyor-belt/internal/utils"
)

// BuildResult represents the result of building a single Lambda package
type BuildResult struct {
	LambdaName string
	ZipData    []byte
	Hash       string
	Error      error
}

// PackageBuilder handles parallel building of Lambda deployment packages
type PackageBuilder struct {
	sourceDir   string
	sharedDirs  []string
	gemDirs     []string
	concurrency int
	config      *DispatcherConfig
}

// PackageBuilderOption is a functional option for configuring PackageBuilder
type PackageBuilderOption func(*PackageBuilder)

// WithConcurrency sets the maximum number of concurrent builds
func WithConcurrency(n int) PackageBuilderOption {
	return func(pb *PackageBuilder) {
		if n > 0 {
			pb.concurrency = n
		}
	}
}

// WithSharedDirs sets the shared directories to include in packages
func WithSharedDirs(dirs []string) PackageBuilderOption {
	return func(pb *PackageBuilder) {
		pb.sharedDirs = dirs
	}
}

// WithGemDirs sets directories to copy into the Docker build context for path-based gems
func WithGemDirs(dirs []string) PackageBuilderOption {
	return func(pb *PackageBuilder) {
		pb.gemDirs = dirs
	}
}

// WithConfig sets the dispatcher config for the builder
func WithConfig(config *DispatcherConfig) PackageBuilderOption {
	return func(pb *PackageBuilder) {
		pb.config = config
	}
}


// NewPackageBuilder creates a new PackageBuilder with the given source directory
func NewPackageBuilder(sourceDir string, opts ...PackageBuilderOption) *PackageBuilder {
	pb := &PackageBuilder{
		sourceDir:   sourceDir,
		sharedDirs:  []string{"models", "lib", "helpers", "templates"},
		concurrency: runtime.NumCPU(),
	}

	for _, opt := range opts {
		opt(pb)
	}

	return pb
}

// BuildPackages builds Lambda packages in parallel for the given lambda names.
// Gems are built once via Docker and shared across all packages.
// Returns a map of lambda name to BuildResult.
func (pb *PackageBuilder) BuildPackages(ctx context.Context, lambdaNames []string) map[string]*BuildResult {
	if len(lambdaNames) == 0 {
		return make(map[string]*BuildResult)
	}

	// Step 1: Build gems once in a shared directory
	sharedVendorDir, err := pb.buildSharedGems(ctx)
	if err != nil {
		// Return error for all lambdas
		results := make(map[string]*BuildResult, len(lambdaNames))
		for _, name := range lambdaNames {
			results[name] = &BuildResult{
				LambdaName: name,
				Error:      fmt.Errorf("shared gem build failed: %w", err),
			}
		}
		return results
	}
	defer os.RemoveAll(sharedVendorDir)

	// Step 2: Pre-copy shared directories once
	sharedDirsCache, err := pb.cacheSharedDirs(ctx)
	if err != nil {
		utils.Warn(ctx, "Failed to cache shared directories, will copy per-lambda", map[string]interface{}{
			"error": err.Error(),
		})
	}
	if sharedDirsCache != "" {
		defer os.RemoveAll(sharedDirsCache)
	}

	// Step 3: Build individual packages in parallel (no Docker needed)
	results := make(map[string]*BuildResult)
	resultChan := make(chan *BuildResult, len(lambdaNames))
	sem := make(chan struct{}, pb.concurrency)

	var wg sync.WaitGroup
	for _, name := range lambdaNames {
		wg.Add(1)
		go func(lambdaName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := &BuildResult{LambdaName: lambdaName}

			utils.Info(ctx, "Building Lambda package", map[string]interface{}{
				"lambda": lambdaName,
			})

			zipData, err := pb.buildSinglePackageWithSharedGems(ctx, lambdaName, sharedVendorDir, sharedDirsCache)
			if err != nil {
				result.Error = err
				utils.Error(ctx, "Lambda package build failed", map[string]interface{}{
					"lambda": lambdaName,
					"error":  err.Error(),
				})
			} else {
				result.ZipData = zipData
				result.Hash = calculateZipHash(zipData)
				utils.Info(ctx, "Lambda package built successfully", map[string]interface{}{
					"lambda":   lambdaName,
					"zip_size": len(zipData),
					"hash":     result.Hash,
				})
			}

			resultChan <- result
		}(name)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results[result.LambdaName] = result
	}

	return results
}

// resolveGemfilePath finds the Gemfile, checking sourceDir first then the project root (parent dir).
func (pb *PackageBuilder) resolveGemfilePath() string {
	candidate := filepath.Join(pb.sourceDir, "Gemfile")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	candidate = filepath.Join(filepath.Dir(pb.sourceDir), "Gemfile")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return filepath.Join(pb.sourceDir, "Gemfile")
}

// resolveGemfileLockPath finds the Gemfile.lock alongside the resolved Gemfile.
func (pb *PackageBuilder) resolveGemfileLockPath() string {
	return filepath.Join(filepath.Dir(pb.resolveGemfilePath()), "Gemfile.lock")
}

// buildSharedGems runs Docker once to install gems, returns path to vendor directory
func (pb *PackageBuilder) buildSharedGems(ctx context.Context) (string, error) {
	gemfilePath := pb.resolveGemfilePath()

	// Create a temporary directory for the shared gem build
	sharedBuildDir, err := os.MkdirTemp("", "dispatcher-gems-*")
	if err != nil {
		return "", fmt.Errorf("failed to create shared gem build directory: %w", err)
	}

	// Copy Gemfile (or create default) and Gemfile.lock if present
	if _, err := os.Stat(gemfilePath); err == nil {
		if err := pb.copyFile(gemfilePath, filepath.Join(sharedBuildDir, "Gemfile")); err != nil {
			os.RemoveAll(sharedBuildDir)
			return "", fmt.Errorf("failed to copy Gemfile: %w", err)
		}
		lockfilePath := pb.resolveGemfileLockPath()
		if _, err := os.Stat(lockfilePath); err == nil {
			if err := pb.copyFile(lockfilePath, filepath.Join(sharedBuildDir, "Gemfile.lock")); err != nil {
				os.RemoveAll(sharedBuildDir)
				return "", fmt.Errorf("failed to copy Gemfile.lock: %w", err)
			}
		}
	} else {
		defaultGemfile := `source 'https://rubygems.org'

gem 'aws-sdk-dynamodb', '~> 1.0'
gem 'aws-sdk-secretsmanager', '~> 1.0'
gem 'aws-sdk-cognitoidentityprovider', '~> 1.0'
gem 'json', '~> 2.0'
`
		if err := os.WriteFile(filepath.Join(sharedBuildDir, "Gemfile"), []byte(defaultGemfile), 0644); err != nil {
			os.RemoveAll(sharedBuildDir)
			return "", fmt.Errorf("failed to write default Gemfile: %w", err)
		}
	}

	// Copy gem directories for path-based gems (from lambda_gem_dirs)
	for _, gemDir := range pb.gemDirs {
		srcPath := filepath.Join(pb.sourceDir, gemDir)
		destPath := filepath.Join(sharedBuildDir, gemDir)
		if info, err := os.Stat(srcPath); err == nil && info.IsDir() {
			if err := pb.copyDirectory(srcPath, destPath); err != nil {
				os.RemoveAll(sharedBuildDir)
				return "", fmt.Errorf("failed to copy gem directory %q: %w", gemDir, err)
			}
			utils.Info(ctx, "Copied gem directory into Docker build context", map[string]interface{}{
				"gem_dir": gemDir,
			})
		} else if err != nil {
			os.RemoveAll(sharedBuildDir)
			return "", fmt.Errorf("gem directory %q does not exist in %s", gemDir, pb.sourceDir)
		}
	}

	// Auto-detect vendor/cache for pre-built .gem files
	vendorCachePath := filepath.Join(pb.sourceDir, "vendor", "cache")
	if info, err := os.Stat(vendorCachePath); err == nil && info.IsDir() {
		destPath := filepath.Join(sharedBuildDir, "vendor", "cache")
		if err := pb.copyDirectory(vendorCachePath, destPath); err != nil {
			os.RemoveAll(sharedBuildDir)
			return "", fmt.Errorf("failed to copy vendor/cache: %w", err)
		}
		utils.Info(ctx, "Copied vendor/cache into Docker build context", nil)
	}

	utils.Info(ctx, "Running Docker once to build shared gems for all Lambdas", map[string]interface{}{
		"build_dir": sharedBuildDir,
	})

	absBuildDir, err := filepath.Abs(sharedBuildDir)
	if err != nil {
		os.RemoveAll(sharedBuildDir)
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	uid := os.Getuid()
	gid := os.Getgid()
	dockerCmd := exec.Command("docker", "run", "--rm",
		"--platform", "linux/amd64",
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"-e", "HOME=/tmp",
		"-v", fmt.Sprintf("%s:/var/task", absBuildDir),
		"-w", "/var/task",
		"public.ecr.aws/sam/build-ruby3.4:latest-x86_64",
		"/bin/bash", "-c", `bundle config set --local path 'vendor/bundle' && \
bundle config set --local without 'development test' && \
bundle config set silence_root_warning 1 && \
bundle install --jobs 4 && \
bundle clean --force`,
	)

	output, err := dockerCmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(sharedBuildDir)
		return "", fmt.Errorf("failed to build gems with Docker: %w\nOutput: %s", err, string(output))
	}

	// Strip vendor fat from the shared gem build
	pb.stripVendorFat(ctx, sharedBuildDir)

	utils.Info(ctx, "Successfully built shared gems with Docker", nil)
	return sharedBuildDir, nil
}

// cacheSharedDirs copies shared directories once to a temp location for reuse
func (pb *PackageBuilder) cacheSharedDirs(ctx context.Context) (string, error) {
	cacheDir, err := os.MkdirTemp("", "dispatcher-shared-*")
	if err != nil {
		return "", err
	}

	found := false
	for _, dirName := range pb.sharedDirs {
		sharedDirPath := filepath.Join(pb.sourceDir, dirName)
		if info, err := os.Stat(sharedDirPath); err == nil && info.IsDir() {
			if err := pb.copyDirectory(sharedDirPath, filepath.Join(cacheDir, dirName)); err != nil {
				utils.Warn(ctx, "Failed to cache shared directory", map[string]interface{}{
					"directory": dirName,
					"error":     err.Error(),
				})
				continue
			}
			found = true
		}
	}

	if !found {
		os.RemoveAll(cacheDir)
		return "", nil
	}
	return cacheDir, nil
}

// buildSinglePackageWithSharedGems builds a Lambda package using pre-built gems
func (pb *PackageBuilder) buildSinglePackageWithSharedGems(ctx context.Context, lambda, sharedVendorDir, sharedDirsCache string) ([]byte, error) {
	rubyFileName := fmt.Sprintf("%s.rb", lambda)
	rubyFilePath := filepath.Join(pb.sourceDir, rubyFileName)

	buildDir, err := os.MkdirTemp("", fmt.Sprintf("dispatcher-pkg-%s-*", lambda))
	if err != nil {
		return nil, fmt.Errorf("failed to create build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	// Copy Ruby file or create placeholder
	if _, err := os.Stat(rubyFilePath); err == nil {
		if err := pb.copyFile(rubyFilePath, filepath.Join(buildDir, rubyFileName)); err != nil {
			return nil, fmt.Errorf("failed to copy Ruby file: %w", err)
		}
	} else {
		placeholderCode := pb.generatePlaceholderCode(lambda)
		if err := os.WriteFile(filepath.Join(buildDir, rubyFileName), []byte(placeholderCode), 0644); err != nil {
			return nil, fmt.Errorf("failed to write placeholder Ruby file: %w", err)
		}
	}

	// Copy shared directories from cache (or source if no cache)
	if sharedDirsCache != "" {
		for _, dirName := range pb.sharedDirs {
			cachedDir := filepath.Join(sharedDirsCache, dirName)
			if info, err := os.Stat(cachedDir); err == nil && info.IsDir() {
				if err := pb.copyDirectory(cachedDir, filepath.Join(buildDir, dirName)); err != nil {
					utils.Warn(ctx, "Failed to copy cached shared directory", map[string]interface{}{
						"directory": dirName,
						"error":     err.Error(),
					})
				}
			}
		}
	} else {
		for _, dirName := range pb.sharedDirs {
			sharedDirPath := filepath.Join(pb.sourceDir, dirName)
			if info, err := os.Stat(sharedDirPath); err == nil && info.IsDir() {
				if err := pb.copyDirectory(sharedDirPath, filepath.Join(buildDir, dirName)); err != nil {
					utils.Warn(ctx, "Failed to copy shared directory", map[string]interface{}{
						"directory": dirName,
						"error":     err.Error(),
					})
				}
			}
		}
	}

	// Copy Gemfile and pre-built vendor/bundle from shared build
	gemfileSrc := filepath.Join(sharedVendorDir, "Gemfile")
	if _, err := os.Stat(gemfileSrc); err == nil {
		if err := pb.copyFile(gemfileSrc, filepath.Join(buildDir, "Gemfile")); err != nil {
			return nil, fmt.Errorf("failed to copy Gemfile: %w", err)
		}
	}

	// Copy .bundle config
	bundleConfigSrc := filepath.Join(sharedVendorDir, ".bundle")
	if info, err := os.Stat(bundleConfigSrc); err == nil && info.IsDir() {
		if err := pb.copyDirectory(bundleConfigSrc, filepath.Join(buildDir, ".bundle")); err != nil {
			return nil, fmt.Errorf("failed to copy .bundle config: %w", err)
		}
	}

	// Copy vendor/bundle
	vendorSrc := filepath.Join(sharedVendorDir, "vendor")
	if info, err := os.Stat(vendorSrc); err == nil && info.IsDir() {
		if err := pb.copyDirectory(vendorSrc, filepath.Join(buildDir, "vendor")); err != nil {
			return nil, fmt.Errorf("failed to copy vendor bundle: %w", err)
		}
	}

	// Create ZIP
	zipData, err := pb.createZipFromDirectory(buildDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create ZIP file: %w", err)
	}

	return zipData, nil
}

// buildSinglePackage builds a Lambda deployment package for a single lambda
func (pb *PackageBuilder) buildSinglePackage(ctx context.Context, lambda string) ([]byte, error) {
	rubyFileName := fmt.Sprintf("%s.rb", lambda)
	rubyFilePath := filepath.Join(pb.sourceDir, rubyFileName)
	gemfilePath := pb.resolveGemfilePath()

	// Check if actual Ruby file exists
	rubyFileExists := false
	if _, err := os.Stat(rubyFilePath); err == nil {
		rubyFileExists = true
	}

	// Create temporary build directory
	buildDir := filepath.Join(pb.sourceDir, fmt.Sprintf("build-ruby-%s", lambda))

	// Clean up any existing build directory
	os.RemoveAll(buildDir)

	// Create fresh build directory
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create build directory: %w", err)
	}

	// Cleanup build directory when done
	defer os.RemoveAll(buildDir)

	// Copy Ruby file to build directory
	if rubyFileExists {
		if err := pb.copyFile(rubyFilePath, filepath.Join(buildDir, rubyFileName)); err != nil {
			return nil, fmt.Errorf("failed to copy Ruby file: %w", err)
		}
	} else {
		// Create placeholder Ruby file
		placeholderCode := pb.generatePlaceholderCode(lambda)
		if err := os.WriteFile(filepath.Join(buildDir, rubyFileName), []byte(placeholderCode), 0644); err != nil {
			return nil, fmt.Errorf("failed to write placeholder Ruby file: %w", err)
		}
	}

	// Copy shared directories (models, lib, etc.) if they exist
	for _, dirName := range pb.sharedDirs {
		sharedDirPath := filepath.Join(pb.sourceDir, dirName)
		absSharedPath, _ := filepath.Abs(sharedDirPath)

		utils.Debug(ctx, "Checking for shared directory", map[string]interface{}{
			"directory": dirName,
			"path":      sharedDirPath,
			"abs_path":  absSharedPath,
		})

		if info, err := os.Stat(sharedDirPath); err == nil && info.IsDir() {
			destDir := filepath.Join(buildDir, dirName)
			utils.Info(ctx, "Found shared directory, copying to Lambda package", map[string]interface{}{
				"directory": dirName,
				"source":    absSharedPath,
				"dest":      destDir,
			})

			if err := pb.copyDirectory(sharedDirPath, destDir); err != nil {
				utils.Warn(ctx, "Failed to copy shared directory", map[string]interface{}{
					"directory": dirName,
					"error":     err.Error(),
				})
			} else {
				utils.Info(ctx, "Successfully copied shared directory to Lambda package", map[string]interface{}{
					"directory": dirName,
				})
			}
		}
	}

	// Copy or create Gemfile (and Gemfile.lock if present)
	gemfileExists := false
	if _, err := os.Stat(gemfilePath); err == nil {
		gemfileExists = true
		if err := pb.copyFile(gemfilePath, filepath.Join(buildDir, "Gemfile")); err != nil {
			return nil, fmt.Errorf("failed to copy Gemfile: %w", err)
		}
		lockfilePath := pb.resolveGemfileLockPath()
		if _, err := os.Stat(lockfilePath); err == nil {
			if err := pb.copyFile(lockfilePath, filepath.Join(buildDir, "Gemfile.lock")); err != nil {
				return nil, fmt.Errorf("failed to copy Gemfile.lock: %w", err)
			}
		}
	}

	if !gemfileExists {
		// Create default Gemfile
		defaultGemfile := `source 'https://rubygems.org'

gem 'aws-sdk-dynamodb', '~> 1.0'
gem 'aws-sdk-secretsmanager', '~> 1.0'
gem 'aws-sdk-cognitoidentityprovider', '~> 1.0'
gem 'json', '~> 2.0'
`
		if err := os.WriteFile(filepath.Join(buildDir, "Gemfile"), []byte(defaultGemfile), 0644); err != nil {
			return nil, fmt.Errorf("failed to write default Gemfile: %w", err)
		}
	}

	// Build gems in Docker (Lambda-compatible environment)
	utils.Info(ctx, "Running Docker to build gems for Lambda compatibility", map[string]interface{}{
		"build_dir": buildDir,
		"lambda":    lambda,
	})

	// Get absolute path for Docker volume mount
	absBuildDir, err := filepath.Abs(buildDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Run Docker to install gems in Lambda-compatible environment
	uid := os.Getuid()
	gid := os.Getgid()
	dockerCmd := exec.Command("docker", "run", "--rm",
		"--platform", "linux/amd64",
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"-e", "HOME=/tmp",
		"-v", fmt.Sprintf("%s:/var/task", absBuildDir),
		"-w", "/var/task",
		"public.ecr.aws/sam/build-ruby3.4:latest-x86_64",
		"/bin/bash", "-c", `bundle config set --local path 'vendor/bundle' && \
bundle config set --local without 'development test' && \
bundle config set silence_root_warning 1 && \
bundle install --jobs 4 && \
bundle clean --force`,
	)

	output, err := dockerCmd.CombinedOutput()
	if err != nil {
		utils.Error(ctx, "Docker build failed", map[string]interface{}{
			"error":  err.Error(),
			"output": string(output),
			"lambda": lambda,
		})
		return nil, fmt.Errorf("failed to build gems with Docker: %w\nOutput: %s", err, string(output))
	}

	utils.Info(ctx, "Successfully built gems with Docker", map[string]interface{}{
		"lambda": lambda,
	})

	// Strip vendor fat from the build directory
	pb.stripVendorFat(ctx, buildDir)

	// Create ZIP file from build directory
	zipData, err := pb.createZipFromDirectory(buildDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create ZIP file: %w", err)
	}

	utils.Info(ctx, "Successfully created Lambda deployment package", map[string]interface{}{
		"lambda":   lambda,
		"zip_size": len(zipData),
	})

	return zipData, nil
}


// copyFile copies a file from src to dst
func (pb *PackageBuilder) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// copyDirectory recursively copies a directory from src to dst
func (pb *PackageBuilder) copyDirectory(src, dst string) error {
	// Get info about source directory
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination directory with same permissions
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	// Read directory contents
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	// Copy each entry
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Recursively copy subdirectory
			if err := pb.copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := pb.copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// stripVendorFat removes test suites, docs, C sources, build artifacts, and debug symbols
// from vendor/bundle to reduce Lambda package size.
func (pb *PackageBuilder) stripVendorFat(ctx context.Context, buildDir string) {
	vendorDir := filepath.Join(buildDir, "vendor")
	if _, err := os.Stat(vendorDir); err != nil {
		return
	}

	// Directories to remove entirely (only at gem root level, not inside lib/)
	removeDirs := map[string]bool{
		"spec": true, "test": true, "tests": true,
		"doc": true, "docs": true, "examples": true, "benchmarks": true,
	}

	// File patterns to remove (conservative: only build artifacts and documentation)
	removeExts := map[string]bool{
		".rdoc": true,
		".c": true, ".h": true, ".o": true,
	}
	removeNames := map[string]bool{
		"Makefile": true, "Rakefile": true,
		".gitignore": true, ".travis.yml": true, ".rubocop.yml": true,
	}

	var soFiles []string

	// Find gem root directories (vendor/bundle/ruby/X.Y.Z/gems/gemname-version/)
	// Only remove fat directories at the gem root level to avoid breaking gems
	// that use directory names like "test" or "doc" inside their lib/ tree.
	gemsGlob := filepath.Join(vendorDir, "bundle", "ruby", "*", "gems", "*")
	gemDirs, _ := filepath.Glob(gemsGlob)

	for _, gemDir := range gemDirs {
		// Remove top-level fat directories from each gem
		for dir := range removeDirs {
			dirPath := filepath.Join(gemDir, dir)
			if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
				os.RemoveAll(dirPath)
			}
		}
	}

	// Walk the entire vendor tree for file-level cleanup
	filepath.Walk(vendorDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()

		// Check extensions
		if removeExts[filepath.Ext(name)] {
			os.Remove(path)
			return nil
		}

		// Check exact names
		if removeNames[name] {
			os.Remove(path)
			return nil
		}

		// Collect .so files for stripping
		if filepath.Ext(name) == ".so" {
			soFiles = append(soFiles, path)
		}

		return nil
	})

	// Strip debug symbols from native extensions
	for _, soFile := range soFiles {
		exec.Command("strip", "--strip-debug", soFile).Run()
	}

	utils.Info(ctx, "Stripped vendor bundle fat", map[string]interface{}{
		"build_dir": buildDir,
	})
}

// generatePlaceholderCode generates placeholder Ruby code when actual file doesn't exist
func (pb *PackageBuilder) generatePlaceholderCode(lambda string) string {
	return fmt.Sprintf(`require 'json'
require 'aws-sdk-dynamodb'

def lambda_handler(event:, context:)
  puts "Event: #{event.to_json}"
  
  # Environment variables
  app_name = ENV['APP_NAME']
  environment = ENV['ENVIRONMENT']
  lambda = ENV['ACTION']
  tables = ENV['TABLES'] ? ENV['TABLES'].split(',') : []
  
  {
    statusCode: 200,
    headers: {
      'Content-Type' => 'application/json',
      'Access-Control-Allow-Origin' => ENV['FRONTEND_URL'] || '*',
      'Access-Control-Allow-Headers' => 'Content-Type,X-Amz-Date,Authorization,X-Api-Key,X-Amz-Security-Token',
      'Access-Control-Allow-Methods' => 'GET,POST,PUT,DELETE,PATCH,OPTIONS'
    },
    body: {
      message: "Hello from %s Ruby Lambda function (placeholder)",
      appName: app_name,
      environment: environment,
      lambda: lambda,
      tables: tables,
      event: event
    }.to_json
  }
end`, lambda)
}

// createZipFromDirectory creates a ZIP file from all contents of a directory
func (pb *PackageBuilder) createZipFromDirectory(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	// Walk the directory tree
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Get relative path from base directory
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip .git files
		if strings.Contains(relPath, ".git") {
			return nil
		}

		// Create zip entry
		writer, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		// Read file content
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Write to zip
		_, err = writer.Write(content)
		return err
	})

	if err != nil {
		return nil, err
	}

	if err := zipWriter.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// calculateZipHash calculates a deterministic hash of zip data
func calculateZipHash(data []byte) string {
	hasher := sha256.New()
	hasher.Write(data)
	return hex.EncodeToString(hasher.Sum(nil))
}

// GetSuccessfulBuilds returns only the successful build results
func (pb *PackageBuilder) GetSuccessfulBuilds(results map[string]*BuildResult) map[string]*BuildResult {
	successful := make(map[string]*BuildResult)
	for name, result := range results {
		if result.Error == nil {
			successful[name] = result
		}
	}
	return successful
}

// GetFailedBuilds returns only the failed build results
func (pb *PackageBuilder) GetFailedBuilds(results map[string]*BuildResult) map[string]*BuildResult {
	failed := make(map[string]*BuildResult)
	for name, result := range results {
		if result.Error != nil {
			failed[name] = result
		}
	}
	return failed
}

// HasFailures checks if any builds failed
func (pb *PackageBuilder) HasFailures(results map[string]*BuildResult) bool {
	for _, result := range results {
		if result.Error != nil {
			return true
		}
	}
	return false
}

// GetFailureErrors returns a formatted error message for all failures
func (pb *PackageBuilder) GetFailureErrors(results map[string]*BuildResult) error {
	failed := pb.GetFailedBuilds(results)
	if len(failed) == 0 {
		return nil
	}

	errorList := make([]string, 0, len(failed))
	for name, result := range failed {
		errorList = append(errorList, fmt.Sprintf("%s: %s", name, result.Error.Error()))
	}
	return fmt.Errorf("failed to build %d Lambda packages: %s", len(failed), strings.Join(errorList, "; "))
}

// Concurrency returns the current concurrency limit
func (pb *PackageBuilder) Concurrency() int {
	return pb.concurrency
}

// SourceDir returns the source directory
func (pb *PackageBuilder) SourceDir() string {
	return pb.sourceDir
}

// SharedDirs returns the shared directories
func (pb *PackageBuilder) SharedDirs() []string {
	return pb.sharedDirs
}
