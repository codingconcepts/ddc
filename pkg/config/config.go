package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RootPaths           []string              `yaml:"root_paths"`
	DockerImagePrefixes map[string]*Threshold `yaml:"docker_image_prefixes"`
	DockerBuildCache    *Threshold            `yaml:"docker_build_cache"`
	DockerVolumePrune   bool                  `yaml:"docker_volume_prune"`
	DeleteDirectories   map[string]Threshold  `yaml:"delete_directories"`
	DeleteFiles         map[string]Threshold  `yaml:"delete_files"`
	DeletePaths         map[string]Threshold  `yaml:"delete_paths"`
	IgnorePaths         []string              `yaml:"ignore_directories"`
	Homebrew            bool                  `yaml:"homebrew_cleanup"`
	GitGC               *Threshold            `yaml:"git_gc"`
	GitCleanBranches    bool                  `yaml:"git_clean_branches"`
	TempCleanup         bool                  `yaml:"temp_cleanup"`
	GoClean             *GoClean              `yaml:"go_cleanup"`
}

type Threshold struct {
	Size int64
	Age  time.Duration
}

type GoClean struct {
	Cache     bool `yaml:"cache"`
	ModCache  bool `yaml:"modcache"`
	TestCache bool `yaml:"testcache"`
	FuzzCache bool `yaml:"fuzzcache"`
}

func (t *Threshold) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	for part := range strings.FieldsSeq(s) {
		if size, err := ParseSize(part); err == nil {
			t.Size = size
		} else if age, err := ParseAge(part); err == nil {
			t.Age = age
		} else {
			return fmt.Errorf("%q is not a valid size or age", part)
		}
	}

	if t.Size == 0 && t.Age == 0 {
		return fmt.Errorf("threshold %q must specify a size or age", s)
	}
	return nil
}

func (t Threshold) String() string {
	var parts []string
	if t.Size > 0 {
		parts = append(parts, FormatSize(t.Size))
	}
	if t.Age > 0 {
		parts = append(parts, FormatAge(t.Age))
	}
	return strings.Join(parts, " ")
}

func FormatSize(b int64) string {
	switch {
	case b >= 1_000_000_000:
		return fmt.Sprintf("%.1fGB", float64(b)/1_000_000_000)
	case b >= 1_000_000:
		return fmt.Sprintf("%.1fMB", float64(b)/1_000_000)
	case b >= 1_000:
		return fmt.Sprintf("%.1fKB", float64(b)/1_000)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func FormatAge(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days >= 365:
		return fmt.Sprintf("%dy", days/365)
	case days >= 30:
		return fmt.Sprintf("%dm", days/30)
	case days >= 7:
		return fmt.Sprintf("%dw", days/7)
	default:
		return fmt.Sprintf("%dd", days)
	}
}

var Default = Config{
	RootPaths: []string{mustGetRootPath()},
}

func mustGetRootPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("getting user home directory: %v", err)
	}

	return home
}

func Ensure(path string) error {
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return yaml.NewEncoder(f).Encode(Default)
}

func Parse(path string) (cfg Config, err error) {
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()

	if err = yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return
	}

	for i, p := range cfg.RootPaths {
		if cfg.RootPaths[i], err = ExpandPath(p); err != nil {
			return
		}
	}
	for i, p := range cfg.IgnorePaths {
		if cfg.IgnorePaths[i], err = ExpandPath(p); err != nil {
			return
		}
	}

	// Lowercase file extension keys so matching is case-insensitive.
	if cfg.DeleteFiles != nil {
		lowerFiles := make(map[string]Threshold, len(cfg.DeleteFiles))
		for k, v := range cfg.DeleteFiles {
			lowerFiles[strings.ToLower(k)] = v
		}
		cfg.DeleteFiles = lowerFiles
	}

	// Expand ~ in delete_paths keys.
	if cfg.DeletePaths != nil {
		expanded := make(map[string]Threshold, len(cfg.DeletePaths))
		for k, v := range cfg.DeletePaths {
			var ep string
			if ep, err = ExpandPath(k); err != nil {
				return
			}
			expanded[ep] = v
		}
		cfg.DeletePaths = expanded
	}

	return
}

func (c Config) Print() {
	log.Println("root_paths")
	for _, path := range c.RootPaths {
		log.Printf("  - %s", path)
	}

	log.Println("docker_image_prefixes")
	for prefix, threshold := range c.DockerImagePrefixes {
		if threshold != nil {
			log.Printf("  - %s (%v)", prefix, threshold)
		} else {
			log.Printf("  - %s", prefix)
		}
	}

	if c.DockerBuildCache != nil {
		log.Printf("docker_build_cache: %v", c.DockerBuildCache)
	}

	if c.DockerVolumePrune {
		log.Println("docker_volume_prune: true")
	}

	log.Println("delete_directories")
	for k, v := range c.DeleteDirectories {
		log.Printf("  - %s (%v)", k, v)
	}

	log.Println("delete_files")
	for k, v := range c.DeleteFiles {
		log.Printf("  - %s (%v)", k, v)
	}

	if len(c.DeletePaths) > 0 {
		log.Println("delete_paths")
		for k, v := range c.DeletePaths {
			log.Printf("  - %s (%v)", k, v)
		}
	}

	log.Println("ignore_directories")
	for _, i := range c.IgnorePaths {
		log.Printf("  - %s", i)
	}

	if c.Homebrew {
		log.Println("homebrew_cleanup: true")
	}

	if c.GitGC != nil {
		log.Printf("git_gc: %v", c.GitGC)
	}

	if c.GitCleanBranches {
		log.Println("git_clean_branches: true")
	}

	if c.TempCleanup {
		log.Println("temp_cleanup: true")
	}

	if c.GoClean != nil {
		log.Println("go_cleanup:")
		if c.GoClean.Cache {
			log.Println("  cache: true")
		}
		if c.GoClean.ModCache {
			log.Println("  modcache: true")
		}
		if c.GoClean.TestCache {
			log.Println("  testcache: true")
		}
		if c.GoClean.FuzzCache {
			log.Println("  fuzzcache: true")
		}
	}
}

func ExpandPath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, path[1:]), nil
}

var sizeSuffixes = []struct {
	suffix string
	mult   int64
}{
	{"TB", 1_000_000_000_000},
	{"GB", 1_000_000_000},
	{"MB", 1_000_000},
	{"KB", 1_000},
	{"B", 1},
}

func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)

	for _, entry := range sizeSuffixes {
		if strings.HasSuffix(upper, entry.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(entry.suffix)])
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			return int64(n * float64(entry.mult)), nil
		}
	}

	return 0, fmt.Errorf("invalid size %q: unknown unit", s)
}

var ageSuffixes = []struct {
	suffix string
	dur    time.Duration
}{
	{"y", 365 * 24 * time.Hour},
	{"m", 30 * 24 * time.Hour},
	{"w", 7 * 24 * time.Hour},
	{"d", 24 * time.Hour},
}

func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)

	for _, entry := range ageSuffixes {
		if strings.HasSuffix(lower, entry.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(entry.suffix)])
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid age %q: %w", s, err)
			}
			return time.Duration(n * float64(entry.dur)), nil
		}
	}

	return 0, fmt.Errorf("invalid age %q: unknown unit", s)
}
