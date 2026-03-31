package config

import (
	"os"
	"path/filepath"
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
