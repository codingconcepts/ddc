package runner

import (
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
	cfg    config.Config
	dryRun bool
	debug  bool
	cancel context.CancelFunc
}

func New(cfg config.Config, dryRun, debug bool) *Runner {
	return &Runner{
		cfg:    cfg,
		dryRun: dryRun,
		debug:  debug,
	}
}

func (r *Runner) Start(ctx context.Context) error {
	ctx, r.cancel = context.WithCancel(ctx)

	var dockerSize int64
	if dockerExists() {
		if err := r.pruneImages(ctx); err != nil {
			return fmt.Errorf("pruning docker images: %w", err)
		}

		for _, prefix := range r.cfg.DockerImagePrefixes {
			n, err := r.removeImages(ctx, prefix)
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

func (r *Runner) removeImages(ctx context.Context, prefix string) (int64, error) {
	listCmd := exec.CommandContext(ctx, "docker", "images", "--format", "{{.Repository}}:{{.Tag}}\t{{.Size}}")
	var out bytes.Buffer
	listCmd.Stdout = &out
	if err := listCmd.Run(); err != nil {
		return 0, fmt.Errorf("listing images: %w", err)
	}

	var images []string
	var totalSize int64
	for line := range strings.SplitSeq(out.String(), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if !strings.HasPrefix(name, prefix) || strings.Contains(name, "<none>") {
			continue
		}
		images = append(images, name)
		totalSize += parseDockerSize(parts[1])
	}

	if len(images) == 0 {
		return 0, nil
	}

	for _, image := range images {
		slog.Info("deleting image", "name", image)
	}

	if r.dryRun {
		return totalSize, nil
	}

	rmiCmd := exec.CommandContext(ctx, "docker", append([]string{"rmi", "-f"}, images...)...)
	return totalSize, rmiCmd.Run()
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

				totalSize += size
				slog.Info("deleting directory", "path", r.displayPath(path), "size", config.FormatSize(size), "age", config.FormatAge(time.Since(info.ModTime())))
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
		totalSize += info.Size()
		slog.Info("deleting file", attrs...)
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
