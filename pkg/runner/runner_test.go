package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codingconcepts/ddc/pkg/config"
)

func TestMatchesPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		{
			name:     "exact base name match",
			path:     "/home/user/project/.git",
			patterns: []string{".git"},
			want:     true,
		},
		{
			name:     "no match",
			path:     "/home/user/project/src",
			patterns: []string{".git", "node_modules"},
			want:     false,
		},
		{
			name:     "path with separator pattern",
			path:     "/home/user/Movies/TV",
			patterns: []string{"Movies/TV"},
			want:     true,
		},
		{
			name:     "empty patterns",
			path:     "/home/user/.git",
			patterns: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPath(tt.path, tt.patterns)
			if got != tt.want {
				t.Errorf("matchesPath(%q, %v) = %v, want %v", tt.path, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestMatchesDirTarget(t *testing.T) {
	targets := map[string]config.Threshold{
		"node_modules": {Size: 10_000_000},
	}

	tests := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{"matches", "/project/node_modules", true},
		{"no match", "/project/src", false},
		{"partial name no match", "/project/node_modules_extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := matchesDirTarget(tt.path, targets)
			if ok != tt.wantOK {
				t.Errorf("matchesDirTarget(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
		})
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()

	// Create files with known sizes.
	writeFile(t, filepath.Join(dir, "a.txt"), 1000)
	writeFile(t, filepath.Join(dir, "b.txt"), 2000)

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "c.txt"), 500)

	got, err := dirSize(dir)
	if err != nil {
		t.Fatalf("dirSize() error: %v", err)
	}

	want := int64(3500)
	if got != want {
		t.Errorf("dirSize() = %d, want %d", got, want)
	}
}

func TestShortPath(t *testing.T) {
	tests := []struct {
		path string
		n    int
		want string
	}{
		{"/a/b/c/d/e", 3, filepath.Join("...", "c", "d", "e")},
		{"/a/b", 3, filepath.Join("a", "b")},
		{"foo", 3, "foo"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := shortPath(tt.path, tt.n)
			if got != tt.want {
				t.Errorf("shortPath(%q, %d) = %q, want %q", tt.path, tt.n, got, tt.want)
			}
		})
	}
}

func TestParseDockerSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"1.23GB", 1_230_000_000},
		{"456MB", 456_000_000},
		{"78.9kB", 78_900},
		{"1024B", 1024},
		{"", 0},
		{"invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseDockerSize(tt.input)
			if got != tt.want {
				t.Errorf("parseDockerSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestWalkRootDeletesFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a .log file over threshold.
	logFile := filepath.Join(dir, "big.log")
	writeFile(t, logFile, 2_000_000)

	// Create a .txt file that should not be deleted.
	txtFile := filepath.Join(dir, "keep.txt")
	writeFile(t, txtFile, 100)

	r := New(config.Config{}, true, false, false)
	fileThresholds := map[string]config.Threshold{
		".log": {Size: 1_000_000},
	}

	size, err := r.walkRoot(context.Background(), dir, nil, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 2_000_000 {
		t.Errorf("reclaimed size = %d, want 2000000", size)
	}

	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Error("expected .log file to be deleted")
	}
	if _, err := os.Stat(txtFile); err != nil {
		t.Error("expected .txt file to still exist")
	}
}

func TestWalkRootQuietPreservesFiles(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "big.log")
	writeFile(t, logFile, 2_000_000)

	r := New(config.Config{}, false, false, false)
	fileThresholds := map[string]config.Threshold{
		".log": {Size: 1_000_000},
	}

	size, err := r.walkRoot(context.Background(), dir, nil, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 2_000_000 {
		t.Errorf("reclaimed size = %d, want 2000000", size)
	}

	if _, err := os.Stat(logFile); err != nil {
		t.Error("preview mode should preserve files")
	}
}

func TestWalkRootDeletesDirectories(t *testing.T) {
	dir := t.TempDir()

	nmDir := filepath.Join(dir, "node_modules")
	if err := os.Mkdir(nmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nmDir, "pkg.js"), 5_000_000)

	dirThresholds := map[string]config.Threshold{
		"node_modules": {Size: 1_000_000},
	}

	r := New(config.Config{}, true, false, false)
	size, err := r.walkRoot(context.Background(), dir, nil, dirThresholds, nil)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 5_000_000 {
		t.Errorf("reclaimed size = %d, want 5000000", size)
	}

	if _, err := os.Stat(nmDir); !os.IsNotExist(err) {
		t.Error("expected node_modules to be deleted")
	}
}

func TestWalkRootSkipsBelowSizeThreshold(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "small.log")
	writeFile(t, logFile, 100)

	r := New(config.Config{}, true, false, false)
	fileThresholds := map[string]config.Threshold{
		".log": {Size: 1_000_000},
	}

	size, err := r.walkRoot(context.Background(), dir, nil, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 0 {
		t.Errorf("reclaimed size = %d, want 0", size)
	}

	if _, err := os.Stat(logFile); err != nil {
		t.Error("file below threshold should be preserved")
	}
}

func TestWalkRootRespectsAgeThreshold(t *testing.T) {
	dir := t.TempDir()

	// Create a recent file (should not be deleted).
	recentFile := filepath.Join(dir, "recent.log")
	writeFile(t, recentFile, 2_000_000)

	// Create an old file (should be deleted).
	oldFile := filepath.Join(dir, "old.log")
	writeFile(t, oldFile, 2_000_000)
	old := time.Now().Add(-400 * 24 * time.Hour)
	os.Chtimes(oldFile, old, old)

	r := New(config.Config{}, true, false, false)
	fileThresholds := map[string]config.Threshold{
		".log": {Size: 1_000_000, Age: 365 * 24 * time.Hour},
	}

	size, err := r.walkRoot(context.Background(), dir, nil, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 2_000_000 {
		t.Errorf("reclaimed size = %d, want 2000000", size)
	}

	if _, err := os.Stat(recentFile); err != nil {
		t.Error("recent file should be preserved")
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should be deleted")
	}
}

func TestWalkRootIgnoresPaths(t *testing.T) {
	dir := t.TempDir()

	ignored := filepath.Join(dir, ".git")
	if err := os.Mkdir(ignored, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ignored, "big.log"), 5_000_000)

	r := New(config.Config{}, true, false, false)
	fileThresholds := map[string]config.Threshold{
		".log": {Size: 1_000_000},
	}

	size, err := r.walkRoot(context.Background(), dir, []string{".git"}, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 0 {
		t.Errorf("reclaimed size = %d, want 0 (ignored path)", size)
	}

	if _, err := os.Stat(filepath.Join(ignored, "big.log")); err != nil {
		t.Error("file inside ignored directory should be preserved")
	}
}

func TestDisplayPath(t *testing.T) {
	r := New(config.Config{}, true, false, false)
	got := r.displayPath("/a/b/c/d/e")
	want := shortPath("/a/b/c/d/e", 3)
	if got != want {
		t.Errorf("displayPath() = %q, want %q", got, want)
	}

	rDebug := New(config.Config{}, false, true, false)
	got = rDebug.displayPath("/a/b/c/d/e")
	if got != "/a/b/c/d/e" {
		t.Errorf("displayPath(debug) = %q, want full path", got)
	}
}

func TestMatchesDirTargetMultiSegment(t *testing.T) {
	targets := map[string]config.Threshold{
		".terraform/providers": {Size: 10_000_000},
		".angular/cache":       {Size: 50_000_000},
	}

	tests := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{"terraform providers match", "/project/.terraform/providers", true},
		{"angular cache match", "/home/user/app/.angular/cache", true},
		{"no match", "/project/.terraform", false},
		{"partial no match", "/project/providers", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := matchesDirTarget(tt.path, targets)
			if ok != tt.wantOK {
				t.Errorf("matchesDirTarget(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
		})
	}
}

func TestCleanPaths(t *testing.T) {
	dir := t.TempDir()

	// Create a directory with files exceeding threshold.
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cacheDir, "data.bin"), 2_000_000)

	cfg := config.Config{
		DeletePaths: map[string]config.Threshold{
			cacheDir: {Size: 1_000_000},
		},
	}
	r := New(cfg, true, false, false)
	if err := r.cleanPaths(context.Background()); err != nil {
		t.Fatalf("cleanPaths() error: %v", err)
	}

	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("expected cache directory to be deleted")
	}
}

func TestCleanPathsRespectsSizeThreshold(t *testing.T) {
	dir := t.TempDir()

	// Create a small directory below threshold.
	cacheDir := filepath.Join(dir, "small-cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cacheDir, "tiny.bin"), 100)

	cfg := config.Config{
		DeletePaths: map[string]config.Threshold{
			cacheDir: {Size: 1_000_000},
		},
	}
	r := New(cfg, true, false, false)
	if err := r.cleanPaths(context.Background()); err != nil {
		t.Fatalf("cleanPaths() error: %v", err)
	}

	if _, err := os.Stat(cacheDir); err != nil {
		t.Error("cache directory below threshold should be preserved")
	}
}

func TestCleanPathsRespectsAgeThreshold(t *testing.T) {
	dir := t.TempDir()

	// Create a recent directory (should not be deleted).
	recentDir := filepath.Join(dir, "recent")
	if err := os.Mkdir(recentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(recentDir, "data.bin"), 2_000_000)

	// Create an old directory (should be deleted).
	oldDir := filepath.Join(dir, "old")
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(oldDir, "data.bin"), 2_000_000)
	old := time.Now().Add(-400 * 24 * time.Hour)
	os.Chtimes(oldDir, old, old)

	cfg := config.Config{
		DeletePaths: map[string]config.Threshold{
			recentDir: {Size: 1_000_000, Age: 365 * 24 * time.Hour},
			oldDir:    {Size: 1_000_000, Age: 365 * 24 * time.Hour},
		},
	}
	r := New(cfg, true, false, false)
	if err := r.cleanPaths(context.Background()); err != nil {
		t.Fatalf("cleanPaths() error: %v", err)
	}

	if _, err := os.Stat(recentDir); err != nil {
		t.Error("recent directory should be preserved")
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old directory should be deleted")
	}
}

func TestCleanPathsPreviewMode(t *testing.T) {
	dir := t.TempDir()

	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cacheDir, "data.bin"), 2_000_000)

	cfg := config.Config{
		DeletePaths: map[string]config.Threshold{
			cacheDir: {Size: 1_000_000},
		},
	}
	r := New(cfg, false, false, false)
	if err := r.cleanPaths(context.Background()); err != nil {
		t.Fatalf("cleanPaths() error: %v", err)
	}

	if _, err := os.Stat(cacheDir); err != nil {
		t.Error("preview mode should preserve cache directory")
	}
}

func TestCleanPathsSkipsMissing(t *testing.T) {
	cfg := config.Config{
		DeletePaths: map[string]config.Threshold{
			"/nonexistent/path/that/does/not/exist": {Size: 1},
		},
	}
	r := New(cfg, true, false, false)
	if err := r.cleanPaths(context.Background()); err != nil {
		t.Fatalf("cleanPaths() should not error on missing paths: %v", err)
	}
}

func TestWalkRootDeletesNewDirectoryTypes(t *testing.T) {
	dir := t.TempDir()

	// Create a .next directory over threshold.
	nextDir := filepath.Join(dir, ".next")
	if err := os.Mkdir(nextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nextDir, "build.js"), 5_000_000)

	// Create a __pycache__ directory over threshold.
	pyDir := filepath.Join(dir, "__pycache__")
	if err := os.Mkdir(pyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pyDir, "module.pyc"), 2_000_000)

	dirThresholds := map[string]config.Threshold{
		".next":       {Size: 1_000_000},
		"__pycache__": {Size: 1_000_000},
	}

	r := New(config.Config{}, true, false, false)
	size, err := r.walkRoot(context.Background(), dir, nil, dirThresholds, nil)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 7_000_000 {
		t.Errorf("reclaimed size = %d, want 7000000", size)
	}

	if _, err := os.Stat(nextDir); !os.IsNotExist(err) {
		t.Error("expected .next to be deleted")
	}
	if _, err := os.Stat(pyDir); !os.IsNotExist(err) {
		t.Error("expected __pycache__ to be deleted")
	}
}

func TestWalkRootDeletesNewFileTypes(t *testing.T) {
	dir := t.TempDir()

	// Create files of various new types over threshold.
	jarFile := filepath.Join(dir, "app.jar")
	writeFile(t, jarFile, 2_000_000)

	coreFile := filepath.Join(dir, "crash.core")
	writeFile(t, coreFile, 2_000_000)

	// Create a .txt file that should not be deleted.
	txtFile := filepath.Join(dir, "keep.txt")
	writeFile(t, txtFile, 2_000_000)

	fileThresholds := map[string]config.Threshold{
		".jar":  {Size: 1_000_000},
		".core": {Size: 1_000_000},
	}

	r := New(config.Config{}, true, false, false)
	size, err := r.walkRoot(context.Background(), dir, nil, nil, fileThresholds)
	if err != nil {
		t.Fatalf("walkRoot() error: %v", err)
	}

	if size != 4_000_000 {
		t.Errorf("reclaimed size = %d, want 4000000", size)
	}

	if _, err := os.Stat(jarFile); !os.IsNotExist(err) {
		t.Error("expected .jar file to be deleted")
	}
	if _, err := os.Stat(coreFile); !os.IsNotExist(err) {
		t.Error("expected .core file to be deleted")
	}
	if _, err := os.Stat(txtFile); err != nil {
		t.Error("expected .txt file to still exist")
	}
}

func TestIsBazelWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		want   bool
	}{
		{"WORKSPACE file", []string{"WORKSPACE"}, true},
		{"WORKSPACE.bazel file", []string{"WORKSPACE.bazel"}, true},
		{"MODULE.bazel file", []string{"MODULE.bazel"}, true},
		{"no marker files", []string{"BUILD", "main.go"}, false},
		{"empty directory", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := isBazelWorkspace(dir)
			if got != tt.want {
				t.Errorf("isBazelWorkspace() = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
