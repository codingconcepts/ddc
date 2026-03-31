package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/codingconcepts/ddc/pkg/config"
)

type Runner struct {
	cfg         config.Config
	dryRun      bool
	debug       bool
	interactive bool
	cancel      context.CancelFunc
	scanner     *bufio.Scanner
}

func New(cfg config.Config, dryRun, debug, interactive bool) *Runner {
	return &Runner{
		cfg:         cfg,
		dryRun:      dryRun,
		debug:       debug,
		interactive: interactive,
		scanner:     bufio.NewScanner(os.Stdin),
	}
}

func (r *Runner) confirm(msg string) bool {
	if !r.interactive {
		return true
	}
	fmt.Printf("%s [y/N] ", msg)
	if !r.scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(r.scanner.Text()))
	return answer == "y" || answer == "yes"
}

func (r *Runner) Start(ctx context.Context) error {
	ctx, r.cancel = context.WithCancel(ctx)

	var dockerSize int64
	if dockerExists() {
		if err := r.pruneImages(ctx); err != nil {
			return fmt.Errorf("pruning docker images: %w", err)
		}

		for prefix, threshold := range r.cfg.DockerImagePrefixes {
			n, err := r.removeImages(ctx, prefix, threshold)
			if err != nil {
				return fmt.Errorf("pruning docker images: %w", err)
			}
			dockerSize += n
		}
	}

	if dockerSize > 0 {
		slog.Info("total docker images reclaimed", "size", config.FormatSize(dockerSize))
	}

	if err := r.cleanFilesystem(ctx); err != nil {
		return fmt.Errorf("cleaning filesystem: %w", err)
	}

	return nil
}

func (r *Runner) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func dockerExists() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		log.Println("docker not found, skipping docker step")
		return false
	}

	if err := exec.Command("docker", "info").Run(); err != nil {
		log.Println("docker daemon not running, skipping docker step")
		return false
	}

	return true
}

func (r *Runner) pruneImages(ctx context.Context) error {
	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "image", "prune", "-f")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (r *Runner) removeImages(ctx context.Context, prefix string, threshold *config.Threshold) (int64, error) {
	listCmd := exec.CommandContext(ctx, "docker", "images", "--format", "{{.ID}}\t{{.Repository}}:{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}")
	var out bytes.Buffer
	listCmd.Stdout = &out
	if err := listCmd.Run(); err != nil {
		return 0, fmt.Errorf("listing images: %w", err)
	}

	type imageInfo struct {
		id   string
		name string
		size int64
		age  time.Duration
	}

	var images []imageInfo
	var totalSize int64
	for line := range strings.SplitSeq(out.String(), "\n") {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		id := parts[0]
		name := parts[1]
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		size := parseDockerSize(parts[2])
		createdAt, err := time.Parse("2006-01-02 15:04:05 -0700 MST", strings.TrimSpace(parts[3]))
		if err != nil {
			slog.Warn("error parsing image creation time", "image", name, "error", err)
			continue
		}
		age := time.Since(createdAt)

		if threshold != nil {
			if threshold.Size > 0 && size < threshold.Size {
				continue
			}
			if threshold.Age > 0 && age < threshold.Age {
				continue
			}
		}

		images = append(images, imageInfo{id: id, name: name, size: size, age: age})
		totalSize += size
	}

	if len(images) == 0 {
		return 0, nil
	}

	var ids []string
	var confirmedSize int64
	for _, img := range images {
		slog.Info("deleting image", "name", img.name, "size", config.FormatSize(img.size), "age", config.FormatAge(img.age))
		if !r.confirm(fmt.Sprintf("delete image %s (%s, %s)?", img.name, config.FormatSize(img.size), config.FormatAge(img.age))) {
			continue
		}
		ids = append(ids, img.id)
		confirmedSize += img.size
	}

	if len(ids) == 0 {
		return 0, nil
	}

	if r.dryRun {
		return confirmedSize, nil
	}

	rmiCmd := exec.CommandContext(ctx, "docker", append([]string{"rmi", "-f"}, ids...)...)
	return confirmedSize, rmiCmd.Run()
}

func (r *Runner) cleanFilesystem(_ context.Context) error {
	if len(r.cfg.DeleteDirectories) == 0 && len(r.cfg.DeleteFiles) == 0 {
		return nil
	}

	var totalSize int64
	for _, rootPath := range r.cfg.RootPaths {
		n, err := r.walkRoot(rootPath, r.cfg.IgnorePaths, r.cfg.DeleteDirectories, r.cfg.DeleteFiles)
		if err != nil {
			return err
		}
		totalSize += n
	}

	if totalSize > 0 {
		slog.Info("total reclaimed", "size", config.FormatSize(totalSize))
	}
	return nil
}

func (r *Runner) walkRoot(rootPath string, ignorePaths []string, dirThresholds map[string]config.Threshold, fileThresholds map[string]config.Threshold) (int64, error) {
	var totalSize int64
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("error accessing path", "path", path, "error", err)
			return nil
		}

		if d.IsDir() {
			if matchesPath(path, ignorePaths) {
				slog.Debug("ignoring path", "path", r.displayPath(path))
				return filepath.SkipDir
			}

			if t, ok := matchesDirTarget(path, dirThresholds); ok {
				size, err := dirSize(path)
				if err != nil {
					slog.Warn("error calculating directory size", "path", path, "error", err)
					return nil
				}

				info, err := d.Info()
				if err != nil {
					slog.Warn("error reading dir info", "path", path, "error", err)
					return nil
				}

				if t.Size > 0 && size < t.Size {
					return filepath.SkipDir
				}
				if t.Age > 0 && time.Since(info.ModTime()) < t.Age {
					return filepath.SkipDir
				}

				slog.Info("deleting directory", "path", r.displayPath(path), "size", config.FormatSize(size), "age", config.FormatAge(time.Since(info.ModTime())))
				if !r.confirm(fmt.Sprintf("delete directory %s (%s, %s)?", r.displayPath(path), config.FormatSize(size), config.FormatAge(time.Since(info.ModTime())))) {
					return filepath.SkipDir
				}
				totalSize += size
				if r.dryRun {
					return filepath.SkipDir
				}
				if err := os.RemoveAll(path); err != nil {
					slog.Warn("error deleting directory", "path", path, "error", err)
					return nil
				}
				return filepath.SkipDir
			}

			return nil
		}

		ext := strings.ToLower(filepath.Ext(d.Name()))
		rule, ok := fileThresholds[ext]
		if !ok {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			slog.Warn("error reading file info", "path", path, "error", err)
			return nil
		}

		if rule.Size > 0 && info.Size() < rule.Size {
			return nil
		}
		if rule.Age > 0 && time.Since(info.ModTime()) < rule.Age {
			return nil
		}

		attrs := []any{"path", r.displayPath(path), "size", config.FormatSize(info.Size()), "age", config.FormatAge(time.Since(info.ModTime()))}
		slog.Info("deleting file", attrs...)
		if !r.confirm(fmt.Sprintf("delete file %s (%s, %s)?", r.displayPath(path), config.FormatSize(info.Size()), config.FormatAge(time.Since(info.ModTime())))) {
			return nil
		}
		totalSize += info.Size()
		if r.dryRun {
			return nil
		}

		if err := os.Remove(path); err != nil {
			slog.Warn("error deleting file", "path", path, "error", err)
		}

		return nil
	})
	return totalSize, err
}

func matchesPath(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(p, string(filepath.Separator)) {
			if strings.HasSuffix(path, string(filepath.Separator)+p) {
				return true
			}
		} else if filepath.Base(path) == p {
			return true
		}
	}
	return false
}

func matchesDirTarget(path string, targets map[string]config.Threshold) (config.Threshold, bool) {
	for name, t := range targets {
		if strings.Contains(name, string(filepath.Separator)) {
			if filepath.Base(path) == name {
				return t, true
			}
			continue
		}
		if strings.HasSuffix(path, string(filepath.Separator)+name) {
			return t, true
		}
	}
	return config.Threshold{}, false
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func (r *Runner) displayPath(path string) string {
	if r.debug {
		return path
	}
	return shortPath(path, 3)
}

func shortPath(path string, n int) string {
	var parts []string
	for range n {
		if path == "." || path == string(filepath.Separator) {
			break
		}
		parts = append([]string{filepath.Base(path)}, parts...)
		path = filepath.Dir(path)
	}
	if path != "." && path != string(filepath.Separator) {
		parts = append([]string{"..."}, parts...)
	}
	return filepath.Join(parts...)
}

// parseDockerSize parses a human-readable size string from docker (e.g. "1.23GB", "456MB", "78.9kB").
func parseDockerSize(s string) int64 {
	s = strings.TrimSpace(s)

	multipliers := []struct {
		suffix string
		factor float64
	}{
		{"GB", 1e9},
		{"MB", 1e6},
		{"kB", 1e3},
		{"B", 1},
	}

	for _, m := range multipliers {
		if !strings.HasSuffix(s, m.suffix) {
			continue
		}
		numStr := strings.TrimSuffix(s, m.suffix)
		v, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0
		}
		return int64(v * m.factor)
	}
	return 0
}
