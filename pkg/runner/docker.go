package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codingconcepts/ddc/pkg/config"
	"github.com/codingconcepts/ddc/pkg/ui"
)

const anonymousVolumeLabel = "com.docker.volume.anonymous"

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

// cleanDocker runs every configured docker cleanup step, reporting disk usage
// before and after so the caller can see what was (or would be) reclaimed.
func (r *Runner) cleanDocker(ctx context.Context) error {
	before := dockerDiskUsage(ctx)
	reportDockerUsage("docker disk usage", before)

	if err := r.pruneDanglingImages(ctx); err != nil {
		return fmt.Errorf("pruning dangling docker images: %w", err)
	}

	if ctx.Err() != nil {
		return nil
	}

	imagesSize, err := r.cleanImages(ctx)
	if err != nil {
		return fmt.Errorf("cleaning docker images: %w", err)
	}

	if ctx.Err() != nil {
		r.addDockerEstimates(imagesSize, 0)
		return nil
	}

	if r.cfg.DockerContainerPrune {
		if err := r.pruneContainers(ctx); err != nil {
			return fmt.Errorf("pruning docker containers: %w", err)
		}
	}

	if ctx.Err() != nil {
		r.addDockerEstimates(imagesSize, 0)
		return nil
	}

	if r.cfg.DockerBuildCache != nil && parseHumanSize(before["Build Cache"].Size) > 0 {
		if err := r.pruneBuilderCache(ctx); err != nil {
			return fmt.Errorf("pruning docker build cache: %w", err)
		}
	}

	if ctx.Err() != nil {
		r.addDockerEstimates(imagesSize, 0)
		return nil
	}

	var volumesSize int64

	if r.cfg.DockerVolumePrune != nil {
		var err error
		if volumesSize, err = r.pruneVolumes(ctx); err != nil {
			return fmt.Errorf("pruning docker volumes: %w", err)
		}
	}

	if ctx.Err() != nil {
		r.addDockerEstimates(imagesSize, volumesSize)
		return nil
	}

	if r.cfg.DockerNetworkPrune {
		if err := r.pruneNetworks(ctx); err != nil {
			return fmt.Errorf("pruning docker networks: %w", err)
		}
	}

	// A second `docker system df` is the only measure of what the prunes
	// actually freed, since none of them report it. A dry run has nothing to
	// compare against, so it falls back to what the image and volume steps
	// counted up as they went.
	if r.dryRun || ctx.Err() != nil {
		r.addDockerEstimates(imagesSize, volumesSize)
		return nil
	}

	after := dockerDiskUsage(ctx)
	if before == nil || after == nil {
		r.addDockerEstimates(imagesSize, volumesSize)
		return nil
	}

	for _, t := range dockerStepTypes {
		r.summary.add(t.step, parseHumanSize(before[t.usage].Size)-parseHumanSize(after[t.usage].Size))
	}

	return nil
}

// addDockerEstimates records what the docker steps counted themselves, used
// when `docker system df` can't measure the difference for real.
func (r *Runner) addDockerEstimates(images, volumes int64) {
	r.summary.add(stepDockerImages, images)
	r.summary.add(stepDockerVolumes, volumes)
}

// dockerStepTypes maps the rows of `docker system df` onto the summary steps
// they account for.
var dockerStepTypes = []struct{ usage, step string }{
	{"Images", stepDockerImages},
	{"Containers", stepDockerContainers},
	{"Local Volumes", stepDockerVolumes},
	{"Build Cache", stepDockerBuildCache},
}

// dockerUsage is one row of `docker system df`.
type dockerUsage struct {
	Type        string `json:"Type"`
	TotalCount  string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// dockerDiskUsage returns `docker system df` keyed by type (Images,
// Containers, Local Volumes, Build Cache). Returns nil on error, as the
// summary is informational and must never block cleanup.
func dockerDiskUsage(ctx context.Context) map[string]dockerUsage {
	cmd := exec.CommandContext(ctx, "docker", "system", "df", "--format", "{{json .}}")

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Debug("error reading docker disk usage", "error", err)
		return nil
	}

	usage := map[string]dockerUsage{}

	for line := range strings.SplitSeq(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var u dockerUsage
		if err := json.Unmarshal([]byte(line), &u); err != nil {
			slog.Debug("error parsing docker disk usage", "line", line, "error", err)
			continue
		}

		usage[u.Type] = u
	}

	return usage
}

func reportDockerUsage(msg string, usage map[string]dockerUsage) {
	for _, t := range []string{"Images", "Containers", "Local Volumes", "Build Cache"} {
		u, ok := usage[t]
		if !ok {
			continue
		}

		slog.Info(msg,
			"type", t,
			"count", u.TotalCount,
			"active", u.Active,
			"size", ui.Size(u.Size),
			"reclaimable", ui.Size(strings.TrimSpace(reclaimableSize(u.Reclaimable))),
		)
	}
}

// reclaimableSize strips the percentage from a reclaimable string such as
// "29.46GB (91%)".
func reclaimableSize(s string) string {
	if i := strings.Index(s, "("); i > 0 {
		return strings.TrimSpace(s[:i])
	}

	return s
}

// dockerImage is a single image, identified by ID. An image may carry several
// tags, so tags are collected per ID rather than per `docker images` row,
// which would otherwise double count shared images.
type dockerImage struct {
	id      string
	tags    []string
	size    int64
	created time.Time
	tagged  time.Time
}

// age reports how long since the image was last tagged locally, which for a
// pulled image is its pull time. Falls back to the image creation date, which
// is when the layers were built upstream and can be far older.
func (i dockerImage) age() time.Duration {
	if !i.tagged.IsZero() {
		return time.Since(i.tagged)
	}

	return time.Since(i.created)
}

func (i dockerImage) name() string {
	if len(i.tags) > 0 {
		return i.tags[0]
	}

	return shortImageID(i.id)
}

func shortImageID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}

	return id
}

// listImages returns every tagged image on the daemon, one entry per unique
// image ID.
func listImages(ctx context.Context) ([]dockerImage, error) {
	cmd := exec.CommandContext(ctx, "docker", "images", "--no-trunc",
		"--format", "{{.ID}}\t{{.Repository}}:{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}")

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	byID := map[string]*dockerImage{}

	var order []string

	for line := range strings.SplitSeq(out.String(), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 4)
		if len(parts) < 4 {
			continue
		}

		id, tag := parts[0], parts[1]
		// Dangling images are handled by `docker image prune`.
		if strings.HasPrefix(tag, "<none>") || strings.HasSuffix(tag, ":<none>") {
			continue
		}

		img, ok := byID[id]
		if !ok {
			created, err := time.Parse("2006-01-02 15:04:05 -0700 MST", strings.TrimSpace(parts[3]))
			if err != nil {
				slog.Warn("error parsing image creation time", "image", ui.Name(tag), "error", err)
				continue
			}

			img = &dockerImage{id: id, size: parseHumanSize(parts[2]), created: created}
			byID[id] = img
			order = append(order, id)
		}

		img.tags = append(img.tags, tag)
	}

	images := make([]dockerImage, 0, len(order))
	for _, id := range order {
		images = append(images, *byID[id])
	}

	applyTagTimes(ctx, images)

	return images, nil
}

// applyTagTimes fills in each image's local tag time (its pull time, for
// pulled images) with a single batched inspect call.
func applyTagTimes(ctx context.Context, images []dockerImage) {
	if len(images) == 0 {
		return
	}

	args := []string{"image", "inspect", "--format", "{{.Id}}\t{{.Metadata.LastTagTime}}"}
	for _, img := range images {
		args = append(args, img.id)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Debug("error inspecting images for tag times", "error", err)
		return
	}

	times := map[string]time.Time{}

	for line := range strings.SplitSeq(out.String(), "\n") {
		id, raw, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}

		t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
		if err != nil || t.IsZero() || t.Year() <= 1 {
			continue
		}

		times[id] = t
	}

	for i := range images {
		if t, ok := times[images[i].id]; ok {
			images[i].tagged = t
		}
	}
}

// inUseImageIDs returns the IDs of images backing a container, running or
// stopped. These are never removed.
func inUseImageIDs(ctx context.Context) map[string]bool {
	inUse := map[string]bool{}

	listCmd := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc")

	var ids bytes.Buffer

	listCmd.Stdout = &ids
	if err := listCmd.Run(); err != nil {
		slog.Warn("error listing containers, treating all images as in use", "error", err)
		return nil
	}

	args := []string{"inspect", "--format", "{{.Image}}"}

	for line := range strings.SplitSeq(ids.String(), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			args = append(args, id)
		}
	}

	if len(args) == 3 {
		return inUse
	}

	cmd := exec.CommandContext(ctx, "docker", args...)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Warn("error inspecting containers, skipping image cleanup", "error", err)
		return nil
	}

	for line := range strings.SplitSeq(out.String(), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			inUse[id] = true
		}
	}

	return inUse
}

func (r *Runner) pruneDanglingImages(ctx context.Context) error {
	count := countLines(ctx, "images", "--filter", "dangling=true", "-q")
	if count == 0 {
		return nil
	}

	slog.Info("pruning dangling docker images", "count", count)

	if !r.confirm("prune dangling docker images?") {
		return nil
	}

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

// cleanImages removes images in three passes, from most to least specific:
//
//  1. superseded versions, when docker_image_keep_latest is set
//  2. images matching a docker_image_prefix_include rule
//  3. everything else, when docker_unused_images is set
//
// Images backing a container are never removed, and either a
// docker_image_prefix_exclude entry or an include rule of "never" pins its
// matches against every pass.
//
// Returns the size of the images removed, which a dry run reports as what it
// would have freed.
func (r *Runner) cleanImages(ctx context.Context) (int64, error) {
	if len(r.cfg.DockerImagePrefixInclude) == 0 && r.cfg.DockerUnusedImages == nil && r.cfg.DockerImageKeepLatest == 0 {
		return 0, nil
	}

	images, err := listImages(ctx)
	if err != nil {
		return 0, err
	}

	inUse := inUseImageIDs(ctx)
	if inUse == nil {
		return 0, nil
	}

	// Tags to remove, keyed by image ID.
	victims := map[string][]victim{}

	if r.cfg.DockerImageKeepLatest > 0 {
		for id, tags := range supersededTags(images, r.cfg.DockerImageKeepLatest) {
			victims[id] = append(victims[id], tags...)
		}
	}

	for _, img := range images {
		if inUse[img.id] {
			slog.Debug("image in use by a container, skipping", "image", ui.Name(img.name()))
			delete(victims, img.id)

			continue
		}

		if r.imageExcluded(img) {
			// An exclude prefix pins an image, including against the
			// keep-latest pass.
			slog.Debug("image pinned by an exclude rule, skipping", "image", ui.Name(img.name()))
			delete(victims, img.id)

			continue
		}

		threshold, matched := r.imageRule(img)
		if matched && threshold != nil && threshold.Never {
			// "never" pins an image, including against the keep-latest pass.
			slog.Debug("image pinned by a never rule, skipping", "image", ui.Name(img.name()))
			delete(victims, img.id)

			continue
		}

		if !matched || !thresholdMet(threshold, img.size, img.age()) {
			continue
		}

		all := make([]victim, 0, len(img.tags))
		for _, tag := range img.tags {
			all = append(all, victim{tag: tag, reason: "unused"})
		}

		victims[img.id] = all
	}

	var totalSize int64

	for _, img := range images {
		if ctx.Err() != nil {
			return totalSize, nil
		}

		tags := victims[img.id]
		if len(tags) == 0 {
			continue
		}

		// Removing only some of an image's tags untags it but frees nothing,
		// so only count the size when every tag goes.
		var size int64
		if len(tags) == len(img.tags) {
			size = img.size
		}

		for i, v := range tags {
			attrs := []any{"image", ui.Name(v.tag), "age", config.FormatAge(img.age()), "reason", v.reason}
			if v.supersededBy != "" {
				attrs = append(attrs, "superseded_by", ui.Name(v.supersededBy))
			}

			if size > 0 {
				// Only the last tag to go frees the image, so only that line
				// carries the size. Repeating it per tag would read as though
				// a multi-tagged image reclaims its size several times over.
				if i == len(tags)-1 {
					attrs = append(attrs, "size", config.FormatSize(size))
				}

				slog.Info("deleting image", attrs...)
			} else {
				slog.Info("untagging image", attrs...)
			}

			if !r.confirm(v.prompt(img)) {
				size = 0
				continue
			}

			if r.dryRun {
				continue
			}

			// Remove by tag rather than ID: an image with tags left over is
			// untagged, and the last tag to go frees it. Removing one tag at a
			// time keeps a single failure from aborting the rest.
			cmd := exec.CommandContext(ctx, "docker", "rmi", v.tag)

			var stderr bytes.Buffer

			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				slog.Warn("error deleting image", "image", ui.Name(v.tag), "error", err, "stderr", strings.TrimSpace(stderr.String()))

				size = 0
			}
		}

		totalSize += size
	}

	return totalSize, nil
}

// imageExcluded reports whether any of an image's tags starts with a
// docker_image_prefix_exclude prefix. An exclude always wins, however much
// longer a matching include prefix is.
func (r *Runner) imageExcluded(img dockerImage) bool {
	for _, tag := range img.tags {
		for _, prefix := range r.cfg.DockerImagePrefixExclude {
			if strings.HasPrefix(tag, prefix) {
				return true
			}
		}
	}

	return false
}

// imageRule finds the rule governing an image: the longest matching prefix
// from docker_image_prefix_include, else the docker_unused_images catch-all. A
// nil threshold with matched=true means "remove unconditionally". An image
// matching docker_image_prefix_exclude has no rule at all, so nothing removes
// it.
func (r *Runner) imageRule(img dockerImage) (threshold *config.Threshold, matched bool) {
	if r.imageExcluded(img) {
		return nil, false
	}

	var longest int

	for _, tag := range img.tags {
		for prefix, t := range r.cfg.DockerImagePrefixInclude {
			if !strings.HasPrefix(tag, prefix) {
				continue
			}

			if matched && len(prefix) <= longest {
				continue
			}

			threshold, matched, longest = t, true, len(prefix)
		}
	}

	if matched {
		return threshold, true
	}

	if r.cfg.DockerUnusedImages != nil {
		return r.cfg.DockerUnusedImages, true
	}

	return nil, false
}

// thresholdMet reports whether something of the given size and age clears a
// threshold. A nil threshold always matches; a "never" threshold never does.
func thresholdMet(t *config.Threshold, size int64, age time.Duration) bool {
	if t == nil {
		return true
	}

	if t.Never {
		return false
	}

	if t.Size > 0 && size < t.Size {
		return false
	}

	if t.Age > 0 && age < t.Age {
		return false
	}

	return true
}

// victim is a tag marked for removal, and why.
type victim struct {
	tag    string
	reason string
	// supersededBy names the newer release that replaced this tag, and is
	// only set when reason is "superseded".
	supersededBy string
}

func (v victim) prompt(img dockerImage) string {
	size, age := config.FormatSize(img.size), config.FormatAge(img.age())
	if v.supersededBy != "" {
		return fmt.Sprintf("delete image %s (%s, %s), superseded by %s?", ui.Name(v.tag), size, age, ui.Name(v.supersededBy))
	}

	return fmt.Sprintf("delete image %s (%s, %s)?", ui.Name(v.tag), size, age)
}

// versionedTag is a tag whose name starts with a version number.
type versionedTag struct {
	tag     string
	id      string
	version []int
}

// supersededTags returns, per image ID, the tags left behind once the newest
// `keep` versions of each repository are retained, each paired with the
// newest release that replaced it.
//
// Only tags that parse as a version are considered, so tags naming a variant
// rather than a release ("latest", "dev", "my-feature") are always kept. Tags
// are grouped by repository *and* variant suffix, so postgres:17 and
// postgres:18.4-alpine3.23 are not treated as versions of one another.
func supersededTags(images []dockerImage, keep int) map[string][]victim {
	groups := map[string][]versionedTag{}

	for _, img := range images {
		for _, tag := range img.tags {
			repo, name := splitRepoTag(tag)

			version, variant, ok := parseVersionTag(name)
			if !ok {
				continue
			}

			key := repo + "\x00" + variant
			groups[key] = append(groups[key], versionedTag{tag: tag, id: img.id, version: version})
		}
	}

	superseded := map[string][]victim{}

	for _, tags := range groups {
		if len(tags) <= keep {
			continue
		}

		sort.Slice(tags, func(i, j int) bool {
			return compareVersions(tags[i].version, tags[j].version) > 0
		})

		// The newest release in the group is what replaced the rest.
		newest := tags[0].tag
		for _, t := range tags[keep:] {
			superseded[t.id] = append(superseded[t.id], victim{
				tag:          t.tag,
				reason:       "superseded",
				supersededBy: newest,
			})
		}
	}

	return superseded
}

// splitRepoTag splits "repo:tag" on its final ":", so that a registry port
// ("localhost:5000/repo:tag") is not mistaken for the tag.
func splitRepoTag(image string) (repo, tag string) {
	i := strings.LastIndex(image, ":")
	if i < 0 {
		return image, ""
	}

	return image[:i], image[i+1:]
}

// parseVersionTag splits a tag name into its leading version number and the
// variant that follows it, e.g. "v26.3.5" -> [26 3 5], "", and
// "18.4-alpine3.23" -> [18 4], "alpine3.23". ok is false for a tag that does
// not start with a number, which marks it as a name rather than a version.
func parseVersionTag(name string) (version []int, variant string, ok bool) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(name, "v"), "V")
	if trimmed == "" {
		return nil, "", false
	}

	end := len(trimmed)
	for i, c := range trimmed {
		if (c < '0' || c > '9') && c != '.' {
			end = i
			break
		}
	}

	head := strings.TrimSuffix(trimmed[:end], ".")
	if head == "" {
		return nil, "", false
	}

	for part := range strings.SplitSeq(head, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, "", false
		}

		version = append(version, n)
	}

	variant = strings.TrimLeft(trimmed[end:], "-_.+")

	return version, variant, true
}

func compareVersions(a, b []int) int {
	for i := range max(len(a), len(b)) {
		var x, y int
		if i < len(a) {
			x = a[i]
		}

		if i < len(b) {
			y = b[i]
		}

		if x != y {
			return x - y
		}
	}

	return 0
}

// countLines runs a docker command and counts its non-empty output lines,
// used to skip prune steps that have nothing to do.
func countLines(ctx context.Context, args ...string) int {
	cmd := exec.CommandContext(ctx, "docker", args...)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Debug("error running docker command", "args", args, "error", err)
		return 0
	}

	var n int

	for line := range strings.SplitSeq(out.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}

	return n
}

func (r *Runner) pruneContainers(ctx context.Context) error {
	count := countLines(ctx, "ps", "-aq", "--filter", "status=exited", "--filter", "status=created", "--filter", "status=dead")
	if count == 0 {
		return nil
	}

	slog.Info("pruning stopped docker containers", "count", count)

	if !r.confirm("prune stopped docker containers?") {
		return nil
	}

	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "container", "prune", "-f")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}

	return nil
}

func (r *Runner) pruneNetworks(ctx context.Context) error {
	slog.Info("pruning unused docker networks")

	if !r.confirm("prune unused docker networks?") {
		return nil
	}

	if r.dryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, "docker", "network", "prune", "-f")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}

	return nil
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

// dockerVolume is one entry of `docker system df -v`.
type dockerVolume struct {
	Name   string `json:"Name"`
	Links  string `json:"Links"`
	Size   string `json:"Size"`
	Labels string `json:"Labels"`
}

func (v dockerVolume) anonymous() bool {
	for label := range strings.SplitSeq(v.Labels, ",") {
		key, _, _ := strings.Cut(label, "=")
		if strings.TrimSpace(key) == anonymousVolumeLabel {
			return true
		}
	}

	return false
}

func (v dockerVolume) unused() bool {
	links, err := strconv.Atoi(strings.TrimSpace(v.Links))
	return err == nil && links == 0
}

// listVolumes returns every volume with its size and link count.
func listVolumes(ctx context.Context) ([]dockerVolume, error) {
	cmd := exec.CommandContext(ctx, "docker", "system", "df", "-v", "--format", "{{json .Volumes}}")

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}

	var volumes []dockerVolume
	if err := json.Unmarshal(out.Bytes(), &volumes); err != nil {
		return nil, fmt.Errorf("parsing volumes: %w", err)
	}

	return volumes, nil
}

// volumeCreatedAt returns the creation time of each named volume, batched into
// a single inspect call.
func volumeCreatedAt(ctx context.Context, names []string) map[string]time.Time {
	if len(names) == 0 {
		return nil
	}

	args := append([]string{"volume", "inspect", "--format", "{{.Name}}\t{{.CreatedAt}}"}, names...)
	cmd := exec.CommandContext(ctx, "docker", args...)

	var out bytes.Buffer

	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		slog.Debug("error inspecting volumes for creation times", "error", err)
		return nil
	}

	created := map[string]time.Time{}

	for line := range strings.SplitSeq(out.String(), "\n") {
		name, raw, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}

		t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if err != nil {
			continue
		}

		created[name] = t
	}

	return created
}

// pruneVolumes removes unused volumes. Anonymous volumes (those docker created
// for a container that did not name them) and named volumes are opted into
// separately, because a named volume is far more likely to hold data someone
// meant to keep. `docker volume prune` alone only ever removes the anonymous
// ones.
//
// Returns the size of the volumes removed, which a dry run reports as what it
// would have freed.
func (r *Runner) pruneVolumes(ctx context.Context) (int64, error) {
	cfg := r.cfg.DockerVolumePrune
	if !cfg.Anonymous && !cfg.Named {
		return 0, nil
	}

	volumes, err := listVolumes(ctx)
	if err != nil {
		return 0, err
	}

	var (
		candidates []dockerVolume
		names      []string
	)

	for _, v := range volumes {
		if !v.unused() {
			continue
		}

		if v.anonymous() && !cfg.Anonymous {
			continue
		}

		if !v.anonymous() && !cfg.Named {
			continue
		}

		candidates = append(candidates, v)
		names = append(names, v.Name)
	}

	created := volumeCreatedAt(ctx, names)

	var totalSize int64

	for _, v := range candidates {
		if ctx.Err() != nil {
			return totalSize, nil
		}

		var age time.Duration
		if t, ok := created[v.Name]; ok {
			age = time.Since(t)
		}

		if cfg.Age > 0 && age < cfg.Age {
			continue
		}

		kind := "named"
		if v.anonymous() {
			kind = "anonymous"
		}

		size := parseHumanSize(v.Size)

		slog.Info("deleting volume", "volume", ui.Name(v.Name), "kind", kind, "size", config.FormatSize(size), "age", config.FormatAge(age))

		if !r.confirm(fmt.Sprintf("delete %s volume %s (%s, %s)?", kind, ui.Name(v.Name), config.FormatSize(size), config.FormatAge(age))) {
			continue
		}

		totalSize += size

		if r.dryRun {
			continue
		}

		cmd := exec.CommandContext(ctx, "docker", "volume", "rm", v.Name)

		var stderr bytes.Buffer

		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			slog.Warn("error deleting volume", "volume", ui.Name(v.Name), "error", err, "stderr", strings.TrimSpace(stderr.String()))

			totalSize -= size
		}
	}

	return totalSize, nil
}

// parseHumanSize parses a human-readable size string of the kind docker and
// brew print (e.g. "1.23GB", "456MB", "78.9kB").
func parseHumanSize(s string) int64 {
	s = strings.TrimSpace(s)

	multipliers := []struct {
		suffix string
		factor float64
	}{
		{"TB", 1e12},
		{"GB", 1e9},
		{"MB", 1e6},
		{"kB", 1e3},
		{"KB", 1e3},
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
