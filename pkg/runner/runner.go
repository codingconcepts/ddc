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
	hasBazel    bool
	cancel      context.CancelFunc
	scanner     *bufio.Scanner
}

func New(cfg config.Config, dryRun, debug, interactive bool) *Runner {
	_, bazelErr := exec.LookPath("bazel")
	return &Runner{
		cfg:         cfg,
		dryRun:      dryRun,
		debug:       debug,
		interactive: interactive,
		hasBazel:    bazelErr == nil,
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

	if dockerExists() {
		var dockerSize int64

		if err := r.pruneImages(ctx); err != nil {
			return fmt.Errorf("pruning docker images: %w", err)
		}

		for prefix, threshold := range r.cfg.DockerImagePrefixes {
			if ctx.Err() != nil {
				return nil
			}
			n, err := r.removeImages(ctx, prefix, threshold)
			if err != nil {
				return fmt.Errorf("pruning docker images: %w", err)
			}
			dockerSize += n
		}

		if dockerSize > 0 {
			slog.Info("total docker images reclaimed", "size", config.FormatSize(dockerSize))
		}

		if ctx.Err() != nil {
			return nil
		}

		if r.cfg.DockerBuildCache != nil {
			if err := r.pruneBuilderCache(ctx); err != nil {
				return fmt.Errorf("pruning docker build cache: %w", err)
			}
		}

		if ctx.Err() != nil {
			return nil
		}

		if r.cfg.DockerVolumePrune {
			if err := r.pruneVolumes(ctx); err != nil {
				return fmt.Errorf("pruning docker volumes: %w", err)
			}
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	if r.cfg.GoClean != nil {
		if err := r.cleanGo(ctx); err != nil {
			return fmt.Errorf("cleaning go caches: %w", err)
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	if r.cfg.Homebrew {
		if err := r.cleanHomebrew(ctx); err != nil {
			return fmt.Errorf("running brew cleanup: %w", err)
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	if err := r.cleanFilesystem(ctx); err != nil {
		return fmt.Errorf("cleaning filesystem: %w", err)
	}

	if ctx.Err() != nil {
		return nil
	}

	if len(r.cfg.DeletePaths) > 0 {
		if err := r.cleanPaths(ctx); err != nil {
			return fmt.Errorf("cleaning explicit paths: %w", err)
		}
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

func (r *Runner) cleanFilesystem(ctx context.Context) error {
	if len(r.cfg.DeleteDirectories) == 0 && len(r.cfg.DeleteFiles) == 0 && r.cfg.GitGC == nil && !r.cfg.GitCleanBranches && r.cfg.BazelClean == nil {
		return nil
	}

	rootPaths := r.cfg.RootPaths
	if r.cfg.TempCleanup {
		rootPaths = append(rootPaths, os.TempDir())
	}

	var totalSize int64
	for _, rootPath := range rootPaths {
		if ctx.Err() != nil {
			break
		}
		n, err := r.walkRoot(ctx, rootPath, r.cfg.IgnorePaths, r.cfg.DeleteDirectories, r.cfg.DeleteFiles)
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

func (r *Runner) walkRoot(ctx context.Context, rootPath string, ignorePaths []string, dirThresholds map[string]config.Threshold, fileThresholds map[string]config.Threshold) (int64, error) {
	var totalSize int64
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}

		if err != nil {
			slog.Debug("skipping inaccessible path", "path", path, "error", err)
			if d == nil || d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if matchesPath(path, ignorePaths) {
				slog.Debug("ignoring path", "path", r.displayPath(path))
				return filepath.SkipDir
			}

			// Detect Bazel workspaces and run bazel clean.
			if r.cfg.BazelClean != nil && r.hasBazel && isBazelWorkspace(path) {
				totalSize += r.bazelClean(ctx, path)
			}

			// Detect git repo roots and run git maintenance.
			if r.cfg.GitGC != nil || r.cfg.GitCleanBranches {
				if info, serr := os.Stat(filepath.Join(path, ".git")); serr == nil && info.IsDir() {
					if r.cfg.GitGC != nil {
						r.gitGC(ctx, path)
					}
					if r.cfg.GitCleanBranches {
						r.gitCleanBranches(ctx, path)
					}
				}
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

func (r *Runner) pruneBuilderCache(ctx context.Context) error {
	args := []string{"builder", "prune", "-f"}
	if r.cfg.DockerBuildCache.Age > 0 {
		hours := int(r.cfg.DockerBuildCache.Age.Hours())
		args = append(args, "--filter", fmt.Sprintf("until=%dh", hours))
	}

	slog.Info("pruning docker build cache")
	if !r.confirm("prune docker build cache?") {
		return nil
	}
	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (r *Runner) pruneVolumes(ctx context.Context) error {
	slog.Info("pruning unused docker volumes")
	if !r.confirm("prune unused docker volumes?") {
		return nil
	}
	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "volume", "prune", "-f")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (r *Runner) cleanHomebrew(ctx context.Context) error {
	if _, err := exec.LookPath("brew"); err != nil {
		slog.Debug("brew not found, skipping")
		return nil
	}

	slog.Info("running brew cleanup")
	if !r.confirm("run brew cleanup?") {
		return nil
	}
	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "brew", "cleanup", "--prune=all")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("brew cleanup completed with errors", "error", err, "stderr", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (r *Runner) cleanPaths(ctx context.Context) error {
	var totalSize int64
	for path, threshold := range r.cfg.DeletePaths {
		if ctx.Err() != nil {
			break
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Debug("path not found, skipping", "path", path)
				continue
			}
			slog.Warn("error accessing path", "path", path, "error", err)
			continue
		}

		var size int64
		if info.IsDir() {
			size, err = dirSize(path)
			if err != nil {
				slog.Warn("error calculating path size", "path", path, "error", err)
				continue
			}
		} else {
			size = info.Size()
		}

		if threshold.Size > 0 && size < threshold.Size {
			continue
		}
		if threshold.Age > 0 && time.Since(info.ModTime()) < threshold.Age {
			continue
		}

		slog.Info("deleting path", "path", r.displayPath(path), "size", config.FormatSize(size), "age", config.FormatAge(time.Since(info.ModTime())))
		if !r.confirm(fmt.Sprintf("delete path %s (%s, %s)?", r.displayPath(path), config.FormatSize(size), config.FormatAge(time.Since(info.ModTime())))) {
			continue
		}
		totalSize += size
		if r.dryRun {
			continue
		}

		if err := os.RemoveAll(path); err != nil {
			slog.Warn("error deleting path", "path", path, "error", err)
		}
	}

	if totalSize > 0 {
		slog.Info("total paths reclaimed", "size", config.FormatSize(totalSize))
	}
	return nil
}

func (r *Runner) bazelClean(ctx context.Context, workspacePath string) int64 {
	args := []string{"clean"}
	label := "bazel clean"
	if r.cfg.BazelClean.ExpungeAsync {
		args = append(args, "--expunge_async")
		label = "bazel clean --expunge_async"
	} else if r.cfg.BazelClean.Expunge {
		args = append(args, "--expunge")
		label = "bazel clean --expunge"
	}

	size := bazelOutputSize(ctx, workspacePath)

	slog.Info("running "+label, "path", r.displayPath(workspacePath), "size", config.FormatSize(size))
	if !r.confirm(fmt.Sprintf("run %s in %s (%s)?", label, r.displayPath(workspacePath), config.FormatSize(size))) {
		return 0
	}
	if r.dryRun {
		return size
	}

	cmd := exec.CommandContext(ctx, "bazel", args...)
	cmd.Dir = workspacePath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("error running "+label, "path", workspacePath, "error", err, "stderr", strings.TrimSpace(stderr.String()))
	}
	return size
}

func bazelOutputSize(ctx context.Context, workspacePath string) int64 {
	cmd := exec.CommandContext(ctx, "bazel", "info", "output_base")
	cmd.Dir = workspacePath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0
	}
	outputBase := strings.TrimSpace(out.String())
	if outputBase == "" {
		return 0
	}
	size, _ := dirSize(outputBase)
	return size
}

func isBazelWorkspace(path string) bool {
	markers := []string{"WORKSPACE", "WORKSPACE.bazel", "MODULE.bazel"}
	for _, m := range markers {
		if info, err := os.Stat(filepath.Join(path, m)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func (r *Runner) cleanGo(ctx context.Context) error {
	if _, err := exec.LookPath("go"); err != nil {
		slog.Debug("go not found, skipping")
		return nil
	}

	type goCache struct {
		name    string
		flag    string
		enabled bool
	}

	caches := []goCache{
		{"go build cache", "-cache", r.cfg.GoClean.Cache},
		{"go module cache", "-modcache", r.cfg.GoClean.ModCache},
		{"go test cache", "-testcache", r.cfg.GoClean.TestCache},
		{"go fuzz cache", "-fuzzcache", r.cfg.GoClean.FuzzCache},
	}

	for _, c := range caches {
		if ctx.Err() != nil {
			break
		}
		if !c.enabled {
			continue
		}

		slog.Info("cleaning " + c.name)
		if !r.confirm("clean " + c.name + "?") {
			continue
		}
		if r.dryRun {
			continue
		}

		cmd := exec.CommandContext(ctx, "go", "clean", c.flag)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			slog.Warn("error cleaning "+c.name, "error", err, "stderr", strings.TrimSpace(stderr.String()))
		}
	}

	return nil
}

func (r *Runner) gitGC(ctx context.Context, repoPath string) {
	gitDir := filepath.Join(repoPath, ".git")
	size, err := dirSize(gitDir)
	if err != nil {
		slog.Warn("error calculating .git size", "path", repoPath, "error", err)
		return
	}

	if r.cfg.GitGC.Size > 0 && size < r.cfg.GitGC.Size {
		return
	}

	slog.Info("running git gc", "path", r.displayPath(repoPath), "git_size", config.FormatSize(size))
	if !r.confirm(fmt.Sprintf("run git gc in %s (.git is %s)?", r.displayPath(repoPath), config.FormatSize(size))) {
		return
	}
	if r.dryRun {
		return
	}

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "gc", "--prune=now")
	if err := cmd.Run(); err != nil {
		slog.Warn("error running git gc", "path", repoPath, "error", err)
	}
}

func (r *Runner) gitCleanBranches(ctx context.Context, repoPath string) {
	// Determine the default branch.
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	var out bytes.Buffer
	cmd.Stdout = &out
	defaultBranch := "main"
	if err := cmd.Run(); err == nil {
		parts := strings.Split(strings.TrimSpace(out.String()), "/")
		if len(parts) > 0 {
			defaultBranch = parts[len(parts)-1]
		}
	}

	// List branches merged into the default branch.
	cmd = exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "--merged", defaultBranch)
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return
	}

	var branches []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" || strings.HasPrefix(branch, "*") {
			continue
		}
		if branch == "main" || branch == "master" || branch == defaultBranch {
			continue
		}
		branches = append(branches, branch)
	}

	for _, branch := range branches {
		if ctx.Err() != nil {
			return
		}
		slog.Info("deleting merged branch", "path", r.displayPath(repoPath), "branch", branch)
		if !r.confirm(fmt.Sprintf("delete merged branch %s in %s?", branch, r.displayPath(repoPath))) {
			continue
		}
		if r.dryRun {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "-d", branch)
		if err := cmd.Run(); err != nil {
			slog.Warn("error deleting branch", "branch", branch, "path", repoPath, "error", err)
		}
	}
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
