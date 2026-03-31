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
	RootPaths           []string               `yaml:"root_paths"`
	DockerImagePrefixes []string               `yaml:"docker_image_prefixes"`
	DeleteDirectories   map[string]Threshold   `yaml:"delete_directories"`
	DeleteFiles         map[string]Threshold   `yaml:"delete_files"`
	IgnorePaths         []string               `yaml:"ignore_directories"`
}

type Threshold struct {
	Size int64
	Age  time.Duration
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

	return
}

func (c Config) Print() {
	log.Println("root_paths")
	for _, path := range c.RootPaths {
		log.Printf("  - %s", path)
	}

	log.Println("docker_image_prefixes")
	for _, prefix := range c.DockerImagePrefixes {
		log.Printf("  - %s", prefix)
	}

	log.Println("delete_directories")
	for k, v := range c.DeleteDirectories {
		log.Printf("  - %s (%v)", k, v)
	}

	log.Println("delete_files")
	for k, v := range c.DeleteFiles {
		log.Printf("  - %s (%v)", k, v)
	}

	log.Println("ignore_directories")
	for _, i := range c.IgnorePaths {
		log.Printf("  - %s", i)
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
