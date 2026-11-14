package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"100B", 100},
		{"1KB", 1_000},
		{"10MB", 10_000_000},
		{"1.5GB", 1_500_000_000},
		{"2TB", 2_000_000_000_000},
		{"0.5MB", 500_000},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if err != nil {
				t.Fatalf("ParseSize(%q) error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSizeCaseInsensitive(t *testing.T) {
	got, err := ParseSize("10mb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != 10_000_000 {
		t.Errorf("got %d, want 10000000", got)
	}
}

func TestParseSizeErrors(t *testing.T) {
	for _, input := range []string{"", "abc", "10XB", "MB"} {
		t.Run(input, func(t *testing.T) {
			_, err := ParseSize(input)
			if err == nil {
				t.Errorf("ParseSize(%q) expected error, got nil", input)
			}
		})
	}
}

func TestParseAge(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"1d", 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"6m", 180 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
		{"1.5y", time.Duration(1.5 * float64(365*24*time.Hour))},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAge(tt.input)
			if err != nil {
				t.Fatalf("ParseAge(%q) error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("ParseAge(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseAgeErrors(t *testing.T) {
	for _, input := range []string{"", "abc", "10x", "d"} {
		t.Run(input, func(t *testing.T) {
			_, err := ParseAge(input)
			if err == nil {
				t.Errorf("ParseAge(%q) expected error, got nil", input)
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{500, "500B"},
		{1_500, "1.5KB"},
		{10_000_000, "10.0MB"},
		{1_500_000_000, "1.5GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatSize(tt.input)
			if got != tt.want {
				t.Errorf("FormatSize(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{3 * 24 * time.Hour, "3d"},
		{14 * 24 * time.Hour, "2w"},
		{60 * 24 * time.Hour, "2m"},
		{400 * 24 * time.Hour, "1y"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatAge(tt.input)
			if got != tt.want {
				t.Errorf("FormatAge(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestThresholdUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantSize int64
		wantAge  time.Duration
	}{
		{
			name:     "size only",
			yaml:     `"10MB"`,
			wantSize: 10_000_000,
		},
		{
			name:    "age only",
			yaml:    `"6m"`,
			wantAge: 180 * 24 * time.Hour,
		},
		{
			name:     "size and age",
			yaml:     `"10MB 6m"`,
			wantSize: 10_000_000,
			wantAge:  180 * 24 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var th Threshold
			if err := yaml.Unmarshal([]byte(tt.yaml), &th); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			if th.Size != tt.wantSize {
				t.Errorf("Size = %d, want %d", th.Size, tt.wantSize)
			}

			if th.Age != tt.wantAge {
				t.Errorf("Age = %v, want %v", th.Age, tt.wantAge)
			}
		})
	}
}

func TestThresholdUnmarshalYAMLErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"invalid token", `"foobar"`},
		{"empty", `""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var th Threshold
			if err := yaml.Unmarshal([]byte(tt.yaml), &th); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestThresholdString(t *testing.T) {
	th := Threshold{Size: 10_000_000, Age: 180 * 24 * time.Hour}

	got := th.String()
	if got != "10.0MB 6m" {
		t.Errorf("got %q, want %q", got, "10.0MB 6m")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"tilde prefix", "~/dev", filepath.Join(home, "dev")},
		{"tilde only", "~", home},
		{"absolute path unchanged", "/usr/local", "/usr/local"},
		{"relative path unchanged", "foo/bar", "foo/bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPath(tt.path)
			if err != nil {
				t.Fatalf("ExpandPath(%q) error: %v", tt.path, err)
			}

			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestEnsureCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")

	if err := Ensure(path); err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if len(cfg.RootPaths) == 0 {
		t.Error("expected default RootPaths to be non-empty")
	}
}

func TestEnsureNoopForExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("root_paths:\n  - /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path); err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if len(cfg.RootPaths) != 1 || cfg.RootPaths[0] != "/tmp" {
		t.Errorf("existing config was overwritten: RootPaths = %v", cfg.RootPaths)
	}
}

func TestParseExpandsPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - ~/dev
  - /absolute/path
ignore_directories:
  - ~/ignored
  - .git
delete_files:
  .log: 1MB
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if cfg.RootPaths[0] != filepath.Join(home, "dev") {
		t.Errorf("RootPaths[0] = %q, want %q", cfg.RootPaths[0], filepath.Join(home, "dev"))
	}

	if cfg.RootPaths[1] != "/absolute/path" {
		t.Errorf("RootPaths[1] = %q, want %q", cfg.RootPaths[1], "/absolute/path")
	}

	if cfg.IgnorePaths[0] != filepath.Join(home, "ignored") {
		t.Errorf("IgnorePaths[0] = %q, want %q", cfg.IgnorePaths[0], filepath.Join(home, "ignored"))
	}

	if cfg.IgnorePaths[1] != ".git" {
		t.Errorf("IgnorePaths[1] = %q, want %q", cfg.IgnorePaths[1], ".git")
	}
}

func TestParseNewConfigFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - /tmp
docker_build_cache: 2w
docker_volume_prune: true
homebrew_cleanup: true
git_gc: 500MB
git_clean_branches: true
delete_directories:
  node_modules: 10MB
  .next: 50MB
delete_files:
  .log: 1MB
delete_paths:
  ~/cache: 100MB 3m
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	// Docker build cache.
	if cfg.DockerBuildCache == nil {
		t.Fatal("DockerBuildCache should not be nil")
	}

	if cfg.DockerBuildCache.Age != 14*24*time.Hour {
		t.Errorf("DockerBuildCache.Age = %v, want 2w", cfg.DockerBuildCache.Age)
	}

	// Docker volume prune. The bool form means anonymous volumes only, which
	// is what `docker volume prune` on its own removes.
	if cfg.DockerVolumePrune == nil {
		t.Fatal("DockerVolumePrune should not be nil")
	}

	if !cfg.DockerVolumePrune.Anonymous {
		t.Error("DockerVolumePrune.Anonymous should be true")
	}

	if cfg.DockerVolumePrune.Named {
		t.Error("DockerVolumePrune.Named should be false for the bool form")
	}

	// Homebrew.
	if !cfg.Homebrew {
		t.Error("Homebrew should be true")
	}

	// Git GC.
	if cfg.GitGC == nil {
		t.Fatal("GitGC should not be nil")
	}

	if cfg.GitGC.Size != 500_000_000 {
		t.Errorf("GitGC.Size = %d, want 500000000", cfg.GitGC.Size)
	}
	// The bare threshold form says nothing about pruning, so git's own
	// default has to stand.
	if cfg.GitGC.PruneNow || cfg.GitGC.Prune != 0 {
		t.Errorf("GitGC prune = %v/%v, want git's default", cfg.GitGC.PruneNow, cfg.GitGC.Prune)
	}

	if _, ok := cfg.GitGC.PruneArg(); ok {
		t.Error("PruneArg() should return nothing for the bare threshold form")
	}

	// Git clean branches.
	if !cfg.GitCleanBranches {
		t.Error("GitCleanBranches should be true")
	}

	// Delete directories.
	if _, ok := cfg.DeleteDirectories[".next"]; !ok {
		t.Error("expected .next in DeleteDirectories")
	}

	// Delete paths (tilde expanded).
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	expandedKey := filepath.Join(home, "cache")
	if _, ok := cfg.DeletePaths[expandedKey]; !ok {
		t.Errorf("expected expanded path %q in DeletePaths, got keys: %v", expandedKey, cfg.DeletePaths)
	}
}

func TestParseDeleteFilesLowercasesKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - /tmp
delete_files:
  .DS_Store: 1B
  .LOG: 10MB
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if _, ok := cfg.DeleteFiles[".ds_store"]; !ok {
		t.Errorf("expected .ds_store key, got keys: %v", cfg.DeleteFiles)
	}

	if _, ok := cfg.DeleteFiles[".log"]; !ok {
		t.Errorf("expected .log key, got keys: %v", cfg.DeleteFiles)
	}
	// Original case keys should not exist.
	if _, ok := cfg.DeleteFiles[".DS_Store"]; ok {
		t.Error(".DS_Store key should have been lowercased")
	}
}

func TestParseDockerImageRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - /tmp
docker_unused_images: 500MB 4w
docker_image_keep_latest: 2
docker_container_prune: true
docker_network_prune: true
docker_image_prefix_include:
  golang: 3m
  cockroachdb: never
  redpandadata:
docker_image_prefix_exclude:
  - keepme/
  - postgres
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if cfg.DockerUnusedImages == nil {
		t.Fatal("DockerUnusedImages should not be nil")
	}

	if cfg.DockerUnusedImages.Size != 500_000_000 {
		t.Errorf("DockerUnusedImages.Size = %d, want 500000000", cfg.DockerUnusedImages.Size)
	}

	if cfg.DockerUnusedImages.Age != 28*24*time.Hour {
		t.Errorf("DockerUnusedImages.Age = %v, want 4w", cfg.DockerUnusedImages.Age)
	}

	if cfg.DockerImageKeepLatest != 2 {
		t.Errorf("DockerImageKeepLatest = %d, want 2", cfg.DockerImageKeepLatest)
	}

	if !cfg.DockerContainerPrune {
		t.Error("DockerContainerPrune should be true")
	}

	if !cfg.DockerNetworkPrune {
		t.Error("DockerNetworkPrune should be true")
	}

	// "never" exempts matches from the catch-all above.
	never, ok := cfg.DockerImagePrefixInclude["cockroachdb"]
	if !ok || never == nil {
		t.Fatal("cockroachdb prefix should be present")
	}

	if !never.Never {
		t.Error("cockroachdb threshold should be Never")
	}

	if never.String() != "never" {
		t.Errorf("Threshold.String() = %q, want %q", never.String(), "never")
	}

	// An empty value still means "remove every match".
	if v, ok := cfg.DockerImagePrefixInclude["redpandadata"]; !ok || v != nil {
		t.Errorf("redpandadata threshold = %v, want nil", v)
	}

	want := []string{"keepme/", "postgres"}
	if !slices.Equal(cfg.DockerImagePrefixExclude, want) {
		t.Errorf("DockerImagePrefixExclude = %v, want %v", cfg.DockerImagePrefixExclude, want)
	}
}

func TestDefaultAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	content := `
age: 1m
root_paths:
  - /tmp
docker_unused_images: 500MB
docker_build_cache: 1w
docker_volume_prune:
  anonymous: true
docker_image_prefix_include:
  golang: 3m
  cockroachdb: never
  redpandadata:
delete_directories:
  node_modules: 10MB
  .venv: 100MB 6m
delete_files:
  .log: 10MB
delete_paths:
  /tmp/cache: 1GB
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	month := 30 * 24 * time.Hour
	if cfg.DefaultAge.Duration() != month {
		t.Fatalf("DefaultAge = %v, want 1m", cfg.DefaultAge)
	}

	// Size-only rules pick the default up.
	if got := cfg.DeleteDirectories["node_modules"].Age; got != month {
		t.Errorf("node_modules age = %v, want 1m", got)
	}

	if got := cfg.DeleteFiles[".log"].Age; got != month {
		t.Errorf(".log age = %v, want 1m", got)
	}

	if got := cfg.DeletePaths["/tmp/cache"].Age; got != month {
		t.Errorf("/tmp/cache age = %v, want 1m", got)
	}

	if got := cfg.DockerUnusedImages.Age; got != month {
		t.Errorf("docker_unused_images age = %v, want 1m", got)
	}

	if got := cfg.DockerVolumePrune.Age; got != month {
		t.Errorf("docker_volume_prune age = %v, want 1m", got)
	}

	// A rule with no threshold at all counts as "not set".
	redpanda, ok := cfg.DockerImagePrefixInclude["redpandadata"]
	if !ok || redpanda == nil {
		t.Fatal("redpandadata should have been given the default")
	}

	if redpanda.Age != month {
		t.Errorf("redpandadata age = %v, want 1m", redpanda.Age)
	}

	// An explicit age wins, and "never" is left alone.
	if got := cfg.DeleteDirectories[".venv"].Age; got != 6*month {
		t.Errorf(".venv age = %v, want 6m", got)
	}

	if got := cfg.DockerImagePrefixInclude["golang"].Age; got != 3*month {
		t.Errorf("golang age = %v, want 3m", got)
	}

	never := cfg.DockerImagePrefixInclude["cockroachdb"]
	if !never.Never || never.Age != 0 {
		t.Errorf("cockroachdb = %+v, want an untouched never rule", never)
	}

	// Sizes survive the fill.
	if got := cfg.DeleteDirectories["node_modules"].Size; got != 10_000_000 {
		t.Errorf("node_modules size = %d, want 10000000", got)
	}

	// docker_build_cache already carries an age, so it keeps it.
	if got := cfg.DockerBuildCache.Age; got != 7*24*time.Hour {
		t.Errorf("docker_build_cache age = %v, want 1w", got)
	}
}

func TestDefaultAgeAbsentChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	content := "root_paths:\n  - /tmp\ndelete_directories:\n  node_modules: 10MB\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if got := cfg.DeleteDirectories["node_modules"].Age; got != 0 {
		t.Errorf("node_modules age = %v, want none", got)
	}
}

func TestParseGitGCMapping(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		size     int64
		prune    time.Duration
		pruneNow bool
		arg      string
		hasArg   bool
	}{
		{
			name:   "threshold only keeps git's default",
			yaml:   "git_gc: 100MB",
			size:   100_000_000,
			hasArg: false,
		},
		{
			name:   "mapping with an age",
			yaml:   "git_gc:\n  size: 100MB\n  prune: 2w",
			size:   100_000_000,
			prune:  14 * 24 * time.Hour,
			arg:    "--prune=14.days.ago",
			hasArg: true,
		},
		{
			name:     "prune now is opt-in",
			yaml:     "git_gc:\n  size: 100MB\n  prune: now",
			size:     100_000_000,
			pruneNow: true,
			arg:      "--prune=now",
			hasArg:   true,
		},
		{
			name:   "mapping without a prune key",
			yaml:   "git_gc:\n  size: 1GB",
			size:   1_000_000_000,
			hasArg: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("root_paths:\n  - /tmp\n"+c.yaml+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := Parse(path)
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}

			if cfg.GitGC == nil {
				t.Fatal("GitGC should not be nil")
			}

			if cfg.GitGC.Size != c.size {
				t.Errorf("Size = %d, want %d", cfg.GitGC.Size, c.size)
			}

			if cfg.GitGC.Prune != c.prune {
				t.Errorf("Prune = %v, want %v", cfg.GitGC.Prune, c.prune)
			}

			if cfg.GitGC.PruneNow != c.pruneNow {
				t.Errorf("PruneNow = %v, want %v", cfg.GitGC.PruneNow, c.pruneNow)
			}

			arg, ok := cfg.GitGC.PruneArg()
			if ok != c.hasArg {
				t.Fatalf("PruneArg() ok = %v, want %v", ok, c.hasArg)
			}

			if arg != c.arg {
				t.Errorf("PruneArg() = %q, want %q", arg, c.arg)
			}
		})
	}
}

func TestParseGitGCBadPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	content := "root_paths:\n  - /tmp\ngit_gc:\n  size: 100MB\n  prune: soon\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Parse(path); err == nil {
		t.Fatal("Parse() should reject an unparseable prune value")
	}
}

func TestParseVolumePruneMapping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - /tmp
docker_volume_prune:
  anonymous: true
  named: true
  age: 4w
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if cfg.DockerVolumePrune == nil {
		t.Fatal("DockerVolumePrune should not be nil")
	}

	if !cfg.DockerVolumePrune.Anonymous {
		t.Error("Anonymous should be true")
	}

	if !cfg.DockerVolumePrune.Named {
		t.Error("Named should be true")
	}

	if cfg.DockerVolumePrune.Age != 28*24*time.Hour {
		t.Errorf("Age = %v, want 4w", cfg.DockerVolumePrune.Age)
	}
}

func TestParseVolumePruneInvalidAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
root_paths:
  - /tmp
docker_volume_prune:
  named: true
  age: bananas
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Parse(path); err == nil {
		t.Error("Parse() should reject an invalid volume prune age")
	}
}
