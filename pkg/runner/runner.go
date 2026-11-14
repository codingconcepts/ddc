package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/codingconcepts/ddc/pkg/config"
	"github.com/codingconcepts/ddc/pkg/ui"
)

type Runner struct {
	cfg         config.Config
	dryRun      bool
	debug       bool
	interactive bool
	hasBazel    bool
	cancel      context.CancelFunc
	scanner     *bufio.Scanner
	summary     *summary
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
		summary:     newSummary(),
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

	// Printed however the run ends, so an interrupted or failed run still
	// accounts for what it had already freed.
	defer r.summary.print(os.Stderr, r.dryRun)

	if dockerExists() {
		if err := r.cleanDocker(ctx); err != nil {
			return err
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

	r.summary.add(stepFilesystem, totalSize)

	return nil
}

func (r *Runner) walkRoot(ctx context.Context, rootPath string, ignorePaths []string, dirThresholds map[string]config.Threshold, fileThresholds map[string]config.Threshold) (int64, error) {
	var totalSize int64

	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}

		if err != nil {
			slog.Debug("skipping inaccessible path", "path", ui.Name(path), "error", err)

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

			// Detect Bazel workspaces and run bazel clean. What it frees is
			// reported as its own summary step rather than counted here,
			// which keeps the walk's total to files and directories deleted.
			if r.cfg.BazelClean != nil && r.hasBazel && isBazelWorkspace(path) {
				r.summary.add(stepBazel, r.bazelClean(ctx, path))
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
				size, err := dirSizeCtx(ctx, path)
				if err != nil {
					slog.Warn("error calculating directory size", "path", ui.Name(path), "error", err)
					return nil
				}

				info, err := d.Info()
				if err != nil {
					slog.Warn("error reading dir info", "path", ui.Name(path), "error", err)
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
					slog.Warn("error deleting directory", "path", ui.Name(path), "error", err)

					totalSize -= size

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
			slog.Warn("error reading file info", "path", ui.Name(path), "error", err)
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
			slog.Warn("error deleting file", "path", ui.Name(path), "error", err)

			totalSize -= info.Size()
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
	return dirSizeCtx(context.Background(), path)
}

func dirSizeCtx(ctx context.Context, path string) (int64, error) {
	var size int64

	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}

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

// displayPath renders a path for logging: shortened unless debug logging is
// on, and coloured so the thing being acted on stands out from the rest of
// the line.
func (r *Runner) displayPath(path string) string {
	if r.debug {
		return ui.Name(path)
	}

	return ui.Name(shortPath(path, 3))
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

func (r *Runner) cleanHomebrew(ctx context.Context) error {
	if _, err := exec.LookPath("brew"); err != nil {
		slog.Debug("brew not found, skipping")
		return nil
	}

	slog.Info("running brew cleanup")

	if !r.confirm("run brew cleanup?") {
		return nil
	}

	args := []string{"cleanup", "--prune=all"}
	if r.dryRun {
		// brew's own --dry-run deletes nothing and still reports what it
		// would free, which is the only way to get a figure for this step
		// without running it for real.
		args = append(args, "--dry-run")
	}

	cmd := exec.CommandContext(ctx, "brew", args...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("brew cleanup completed with errors", "error", err, "stderr", strings.TrimSpace(stderr.String()))
	}

	// The closing line goes to stdout, but brew has moved its progress
	// reporting between streams before now, so both are searched.
	r.summary.add(stepHomebrew, parseBrewFreed(stdout.String()+stderr.String()))

	return nil
}

// brewFreed matches the size in brew cleanup's closing line, e.g. "This
// operation has freed approximately 1.2GB of disk space." (or "would free",
// under --dry-run).
var brewFreed = regexp.MustCompile(`approximately ([0-9.]+\s?[A-Za-z]*B)`)

func parseBrewFreed(out string) int64 {
	m := brewFreed.FindStringSubmatch(out)
	if m == nil {
		return 0
	}

	return parseHumanSize(strings.ReplaceAll(m[1], " ", ""))
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

			slog.Warn("error accessing path", "path", ui.Name(path), "error", err)

			continue
		}

		var size int64
		if info.IsDir() {
			size, err = dirSizeCtx(ctx, path)
			if err != nil {
				slog.Warn("error calculating path size", "path", ui.Name(path), "error", err)
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
			slog.Warn("error deleting path", "path", ui.Name(path), "error", err)

			totalSize -= size
		}
	}

	r.summary.add(stepPaths, totalSize)

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
	if ctx.Err() != nil {
		return 0
	}

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
		slog.Warn("error running "+label, "path", ui.Name(workspacePath), "error", err, "stderr", strings.TrimSpace(stderr.String()))
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

	size, _ := dirSizeCtx(ctx, outputBase)

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
		dir     string
	}

	buildCacheDir, modCacheDir := goEnv(ctx, "GOCACHE"), goEnv(ctx, "GOMODCACHE")

	caches := []goCache{
		{"go build cache", "-cache", r.cfg.GoClean.Cache, buildCacheDir},
		{"go module cache", "-modcache", r.cfg.GoClean.ModCache, modCacheDir},
		{"go test cache", "-testcache", r.cfg.GoClean.TestCache, buildCacheDir},
		{"go fuzz cache", "-fuzzcache", r.cfg.GoClean.FuzzCache, buildCacheDir},
	}

	// `go clean` reports nothing about what it freed, so the caches are
	// measured either side of the run. Sizes are tracked per directory rather
	// than per flag, because the test and fuzz caches live inside the build
	// cache and would otherwise be counted twice.
	before := map[string]int64{}

	for _, c := range caches {
		if !c.enabled || c.dir == "" {
			continue
		}

		if _, ok := before[c.dir]; ok {
			continue
		}

		size, err := dirSizeCtx(ctx, c.dir)
		if err != nil {
			slog.Debug("error sizing go cache", "path", ui.Name(c.dir), "error", err)
			continue
		}

		before[c.dir] = size
	}

	cleaned := map[string]bool{}

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
			continue
		}

		cleaned[c.dir] = true
	}

	if r.dryRun {
		// A dry run frees nothing, so report what a real one would: the whole
		// of any cache that would be emptied outright. -testcache and
		// -fuzzcache only drop part of the build cache, and their share isn't
		// knowable without running them, so they report nothing.
		var estimate int64
		if r.cfg.GoClean.Cache {
			estimate += before[buildCacheDir]
		}

		if r.cfg.GoClean.ModCache {
			estimate += before[modCacheDir]
		}

		r.summary.add(stepGoCaches, estimate)

		return nil
	}

	for dir, size := range before {
		if !cleaned[dir] {
			continue
		}

		after, err := dirSizeCtx(ctx, dir)
		if err != nil {
			slog.Debug("error sizing go cache", "path", ui.Name(dir), "error", err)
			continue
		}

		r.summary.add(stepGoCaches, size-after)
	}

	return nil
}

// goEnv reads a single `go env` value, returning "" when it can't be read, in
// which case the cache it names is left unmeasured rather than failing the
// step.
func goEnv(ctx context.Context, name string) string {
	cmd := exec.CommandContext(ctx, "go", "env", name)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Debug("error reading go env", "name", name, "error", err)
		return ""
	}

	return strings.TrimSpace(out.String())
}

func (r *Runner) gitGC(ctx context.Context, repoPath string) {
	gitDir := filepath.Join(repoPath, ".git")

	size, err := dirSizeCtx(ctx, gitDir)
	if err != nil {
		slog.Warn("error calculating .git size", "path", ui.Name(repoPath), "error", err)
		return
	}

	if ctx.Err() != nil {
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

	// Without an explicit prune policy, git keeps unreachable objects for two
	// weeks, so a bad reset or a dropped stash stays recoverable with
	// `git fsck --lost-found`.
	args := []string{"-C", repoPath, "gc"}
	if prune, ok := r.cfg.GitGC.PruneArg(); ok {
		args = append(args, prune)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	if err := cmd.Run(); err != nil {
		slog.Warn("error running git gc", "path", ui.Name(repoPath), "error", err)
		return
	}

	// How much a repack saves is only knowable afterwards, so .git is sized
	// again. A dry run has nothing to compare against and reports nothing.
	after, err := dirSizeCtx(ctx, gitDir)
	if err != nil {
		slog.Debug("error calculating .git size", "path", ui.Name(repoPath), "error", err)
		return
	}

	r.summary.add(stepGitGC, size-after)
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

		slog.Info("deleting merged branch", "path", r.displayPath(repoPath), "branch", ui.Name(branch))

		if !r.confirm(fmt.Sprintf("delete merged branch %s in %s?", ui.Name(branch), r.displayPath(repoPath))) {
			continue
		}

		if r.dryRun {
			continue
		}

		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "-d", branch)
		if err := cmd.Run(); err != nil {
			slog.Warn("error deleting branch", "branch", ui.Name(branch), "path", ui.Name(repoPath), "error", err)
		}
	}
}
