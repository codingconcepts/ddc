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

	"github.com/codingconcepts/ddc/pkg/ui"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// DefaultAge is the age given to every rule that doesn't carry one of
	// its own, so that a size-only rule can't delete something being worked
	// on today. See applyDefaultAge for exactly what it reaches.
	DefaultAge               Age                   `yaml:"age,omitempty"`
	RootPaths                []string              `yaml:"root_paths"`
	DockerImagePrefixInclude map[string]*Threshold `yaml:"docker_image_prefix_include"`
	DockerImagePrefixExclude []string              `yaml:"docker_image_prefix_exclude"`
	DockerUnusedImages       *Threshold            `yaml:"docker_unused_images"`
	DockerImageKeepLatest    int                   `yaml:"docker_image_keep_latest"`
	DockerBuildCache         *Threshold            `yaml:"docker_build_cache"`
	DockerVolumePrune        *VolumePrune          `yaml:"docker_volume_prune"`
	DockerContainerPrune     bool                  `yaml:"docker_container_prune"`
	DockerNetworkPrune       bool                  `yaml:"docker_network_prune"`
	DeleteDirectories        map[string]Threshold  `yaml:"delete_directories"`
	DeleteFiles              map[string]Threshold  `yaml:"delete_files"`
	DeletePaths              map[string]Threshold  `yaml:"delete_paths"`
	IgnorePaths              []string              `yaml:"ignore_directories"`
	Homebrew                 bool                  `yaml:"homebrew_cleanup"`
	GitGC                    *GitGC                `yaml:"git_gc"`
	GitCleanBranches         bool                  `yaml:"git_clean_branches"`
	TempCleanup              bool                  `yaml:"temp_cleanup"`
	GoClean                  *GoClean              `yaml:"go_cleanup"`
	BazelClean               *BazelClean           `yaml:"bazel_cleanup"`
}

type Threshold struct {
	Size  int64
	Age   time.Duration
	Never bool
}

// Age is a duration written in the config's own age units, e.g. "6m".
type Age time.Duration

func (a *Age) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("age must be a duration such as \"6m\": %w", err)
	}

	d, err := ParseAge(s)
	if err != nil {
		return err
	}

	*a = Age(d)

	return nil
}

// MarshalYAML writes the age back in config units, so a config written by
// Ensure can be read again.
func (a Age) MarshalYAML() (any, error) {
	if a == 0 {
		return nil, nil
	}

	return a.String(), nil
}

func (a Age) Duration() time.Duration { return time.Duration(a) }

func (a Age) String() string { return FormatAge(a.Duration()) }

// applyDefaultAge fills in the top-level age wherever a rule didn't set one.
//
// It deliberately leaves alone: a rule that already names an age, a "never"
// rule, and the rules that have no age to apply (docker_image_keep_latest,
// git_gc, and the boolean steps). A rule written with no threshold at all,
// such as a bare image prefix, counts as "not set" and takes the default.
func (c *Config) applyDefaultAge() {
	age := c.DefaultAge.Duration()
	if age == 0 {
		return
	}

	fill := func(t Threshold) Threshold {
		if t.Never || t.Age > 0 {
			return t
		}

		t.Age = age

		return t
	}

	fillPtr := func(t *Threshold) *Threshold {
		if t == nil {
			return &Threshold{Age: age}
		}

		filled := fill(*t)

		return &filled
	}

	for _, m := range []map[string]Threshold{c.DeleteDirectories, c.DeleteFiles, c.DeletePaths} {
		for k, v := range m {
			m[k] = fill(v)
		}
	}

	for k, v := range c.DockerImagePrefixInclude {
		c.DockerImagePrefixInclude[k] = fillPtr(v)
	}

	if c.DockerUnusedImages != nil {
		c.DockerUnusedImages = fillPtr(c.DockerUnusedImages)
	}

	if c.DockerBuildCache != nil {
		c.DockerBuildCache = fillPtr(c.DockerBuildCache)
	}

	if c.DockerVolumePrune != nil && c.DockerVolumePrune.Age == 0 {
		c.DockerVolumePrune.Age = age
	}
}

// VolumePrune controls which unused docker volumes are removed. Anonymous and
// named volumes are opted into separately: a named volume usually holds data
// someone chose to keep, whereas an anonymous one is a leftover docker created
// on the caller's behalf.
type VolumePrune struct {
	Anonymous bool
	Named     bool
	Age       time.Duration
}

// UnmarshalYAML accepts either a bool, for compatibility with the original
// `docker_volume_prune: true`, or a mapping of the fields above.
func (v *VolumePrune) UnmarshalYAML(value *yaml.Node) error {
	var enabled bool
	if err := value.Decode(&enabled); err == nil {
		// `docker volume prune` only removes anonymous volumes, so that is
		// what the bool form continues to mean.
		v.Anonymous = enabled
		return nil
	}

	var raw struct {
		Anonymous bool   `yaml:"anonymous"`
		Named     bool   `yaml:"named"`
		Age       string `yaml:"age"`
	}
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("docker_volume_prune must be a bool or a mapping: %w", err)
	}

	v.Anonymous, v.Named = raw.Anonymous, raw.Named
	if raw.Age != "" {
		age, err := ParseAge(raw.Age)
		if err != nil {
			return err
		}

		v.Age = age
	}

	return nil
}

func (v VolumePrune) String() string {
	var parts []string
	if v.Anonymous {
		parts = append(parts, "anonymous")
	}

	if v.Named {
		parts = append(parts, "named")
	}

	if v.Age > 0 {
		parts = append(parts, "older than "+FormatAge(v.Age))
	}

	if len(parts) == 0 {
		return "disabled"
	}

	return strings.Join(parts, ", ")
}

// GitGC controls `git gc`. Size is the minimum .git size that makes a
// repository worth collecting; Prune decides how long unreachable objects are
// kept, which is the difference between a mistake being recoverable with
// `git fsck --lost-found` and being gone.
type GitGC struct {
	Threshold

	// Prune is how long an unreachable object survives. Zero means the key
	// was omitted and git's own default (two weeks) applies.
	Prune time.Duration
	// PruneNow is set by `prune: now`, which drops unreachable objects
	// immediately and makes a bad reset or a dropped stash unrecoverable.
	PruneNow bool
}

// UnmarshalYAML accepts either a threshold string, for compatibility with the
// original `git_gc: 100MB`, or a mapping carrying the prune policy too.
func (g *GitGC) UnmarshalYAML(value *yaml.Node) error {
	var threshold Threshold
	if err := threshold.UnmarshalYAML(value); err == nil {
		g.Threshold = threshold
		return nil
	}

	var raw struct {
		Size  string `yaml:"size"`
		Age   string `yaml:"age"`
		Prune string `yaml:"prune"`
	}
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("git_gc must be a threshold or a mapping: %w", err)
	}

	if raw.Size != "" {
		size, err := ParseSize(raw.Size)
		if err != nil {
			return err
		}

		g.Size = size
	}

	if raw.Age != "" {
		age, err := ParseAge(raw.Age)
		if err != nil {
			return err
		}

		g.Age = age
	}

	switch prune := strings.ToLower(strings.TrimSpace(raw.Prune)); prune {
	case "":
		// Left to git, which keeps unreachable objects for two weeks.
	case "now":
		g.PruneNow = true
	default:
		age, err := ParseAge(prune)
		if err != nil {
			return fmt.Errorf("git_gc prune %q must be an age or \"now\"", raw.Prune)
		}

		g.Prune = age
	}

	return nil
}

func (g GitGC) String() string {
	var parts []string
	if t := g.Threshold.String(); t != "" {
		parts = append(parts, t)
	}

	switch {
	case g.PruneNow:
		parts = append(parts, "prune now")
	case g.Prune > 0:
		parts = append(parts, "prune after "+FormatAge(g.Prune))
	default:
		parts = append(parts, "prune after git's default")
	}

	return strings.Join(parts, ", ")
}

// PruneArg returns the `--prune` flag to pass to `git gc`, and whether there
// is one to pass at all.
func (g GitGC) PruneArg() (string, bool) {
	if g.PruneNow {
		return "--prune=now", true
	}

	if g.Prune > 0 {
		return fmt.Sprintf("--prune=%d.days.ago", int(g.Prune.Hours()/24)), true
	}

	return "", false
}

type GoClean struct {
	Cache     bool `yaml:"cache"`
	ModCache  bool `yaml:"modcache"`
	TestCache bool `yaml:"testcache"`
	FuzzCache bool `yaml:"fuzzcache"`
}

type BazelClean struct {
	Clean        bool `yaml:"clean"`
	Expunge      bool `yaml:"expunge"`
	ExpungeAsync bool `yaml:"expunge_async"`
}

func (t *Threshold) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	// "never" pins a rule's matches, exempting them from both any broader
	// catch-all rule and from version-based cleanup.
	if strings.EqualFold(strings.TrimSpace(s), "never") {
		t.Never = true
		return nil
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
	if t.Never {
		return "never"
	}

	var parts []string
	if t.Size > 0 {
		parts = append(parts, FormatSize(t.Size))
	}

	if t.Age > 0 {
		parts = append(parts, FormatAge(t.Age))
	}

	return strings.Join(parts, " ")
}

// FormatSize renders a byte count for display, coloured so that sizes stand
// out wherever they are logged.
func FormatSize(b int64) string {
	switch {
	case b >= 1_000_000_000:
		return ui.Size(fmt.Sprintf("%.1fGB", float64(b)/1_000_000_000))
	case b >= 1_000_000:
		return ui.Size(fmt.Sprintf("%.1fMB", float64(b)/1_000_000))
	case b >= 1_000:
		return ui.Size(fmt.Sprintf("%.1fKB", float64(b)/1_000))
	default:
		return ui.Size(fmt.Sprintf("%dB", b))
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

	cfg.applyDefaultAge()

	return
}

// section separates the blocks of Print with a blank line, without leaving a
// stray one at the top when the first block is the first thing printed.
type section struct{ printed bool }

func (s *section) next() {
	if s.printed {
		log.Println()
	}

	s.printed = true
}

func (c Config) Print() {
	var s section

	if c.DefaultAge > 0 {
		s.next()
		log.Printf("age: %v (applied to every rule that didn't set one)", c.DefaultAge)
	}

	s.next()
	log.Println("root_paths")

	for _, path := range c.RootPaths {
		log.Printf("  - %s", ui.Name(path))
	}

	s.next()
	log.Println("docker_image_prefix_include")

	for prefix, threshold := range c.DockerImagePrefixInclude {
		if threshold != nil {
			log.Printf("  - %s (%v)", ui.Name(prefix), threshold)
		} else {
			log.Printf("  - %s", ui.Name(prefix))
		}
	}

	if len(c.DockerImagePrefixExclude) > 0 {
		s.next()
		log.Println("docker_image_prefix_exclude")

		for _, prefix := range c.DockerImagePrefixExclude {
			log.Printf("  - %s", ui.Name(prefix))
		}
	}

	if c.DockerUnusedImages != nil || c.DockerImageKeepLatest > 0 || c.DockerBuildCache != nil ||
		c.DockerVolumePrune != nil || c.DockerContainerPrune || c.DockerNetworkPrune {
		s.next()
	}

	if c.DockerUnusedImages != nil {
		log.Printf("docker_unused_images: %v", c.DockerUnusedImages)
	}

	if c.DockerImageKeepLatest > 0 {
		log.Printf("docker_image_keep_latest: %d", c.DockerImageKeepLatest)
	}

	if c.DockerBuildCache != nil {
		log.Printf("docker_build_cache: %v", c.DockerBuildCache)
	}

	if c.DockerVolumePrune != nil {
		log.Printf("docker_volume_prune: %v", c.DockerVolumePrune)
	}

	if c.DockerContainerPrune {
		log.Println("docker_container_prune: true")
	}

	if c.DockerNetworkPrune {
		log.Println("docker_network_prune: true")
	}

	s.next()
	log.Println("delete_directories")

	for k, v := range c.DeleteDirectories {
		log.Printf("  - %s (%v)", ui.Name(k), v)
	}

	s.next()
	log.Println("delete_files")

	for k, v := range c.DeleteFiles {
		log.Printf("  - %s (%v)", ui.Name(k), v)
	}

	if len(c.DeletePaths) > 0 {
		s.next()
		log.Println("delete_paths")

		for k, v := range c.DeletePaths {
			log.Printf("  - %s (%v)", ui.Name(k), v)
		}
	}

	s.next()
	log.Println("ignore_directories")

	for _, i := range c.IgnorePaths {
		log.Printf("  - %s", ui.Name(i))
	}

	if c.Homebrew || c.GitGC != nil || c.GitCleanBranches || c.TempCleanup {
		s.next()
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

	if c.BazelClean != nil {
		s.next()
		log.Println("bazel_cleanup:")

		if c.BazelClean.Clean {
			log.Println("  clean: true")
		}

		if c.BazelClean.Expunge {
			log.Println("  expunge: true")
		}

		if c.BazelClean.ExpungeAsync {
			log.Println("  expunge_async: true")
		}
	}

	if c.GoClean != nil {
		s.next()
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
