# ddc

Dev Deep Clean - give your machine a good clear out of everything it's accumulated during development.

ddc scans your filesystem and Docker installation to reclaim disk space by removing:

- Bloated directories like `node_modules` and `.terraform/providers`
- Old or oversized files matching extension-based rules
- Specific absolute paths (caches, package stores, derived data)
- Unused Docker images, either matching configured prefixes or as a catch-all
- Superseded versions of a Docker image, keeping only the newest releases
- Docker build cache, stopped containers, unused networks and unused volumes
- Stale git branches and bloated `.git` directories
- Homebrew caches
- Bazel build outputs in detected workspaces
- Go build, module, test, and fuzz caches
- Temporary files from the OS temp directory

On first run, ddc creates a default config at `~/.ddc/config.yaml` if one doesn't exist.

### Installation

```sh
go install github.com/codingconcepts/ddc@latest
```

### Usage

```
Usage of ddc:
  -config string
        absolute or relative path to the ddc config file (default "~/.ddc/config.yaml")
  -dry-run
        report what would be deleted without deleting anything
  -debug
        enable verbose debug logging
  -interactive
        prompt before each deletion
```

**ddc deletes by default.** Run it with `--dry-run` first to see what a given
config would remove:

```sh
ddc --dry-run
```

Execute the deletions:

```sh
ddc
```

Prompt before each deletion:

```sh
ddc --interactive
```

Run with a specific config file and debug logging:

```sh
ddc --config _example/config.yaml --debug
```

Paths in the config file support `~` expansion, so `~` resolves to your home directory.

### Run summary

Every run ends with a breakdown of what each step reclaimed and a total:

```
reclaimed
  docker images          12.4GB
  docker build cache      3.1GB
  go caches               2.8GB
  git gc                820.5MB
  files and directories   1.9GB
  explicit paths        450.0MB
  ------------------------------
  total reclaimed        21.5GB
```

Steps appear in the order they ran, and a step that freed nothing - or that
was skipped, declined or couldn't be measured - is left out. Under `--dry-run`
the headings read `would reclaim` and `total would reclaim`, and the figures
are what a real run would be expected to free.

How each figure is arrived at:

| Step | Measured by | Under `--dry-run` |
|---|---|---|
| `docker images`, `docker containers`, `docker volumes`, `docker build cache` | `docker system df` before and after the docker step | the sizes of the images and volumes that would go; containers and build cache report nothing |
| `go caches` | the size of `GOCACHE` and `GOMODCACHE` before and after `go clean` | the whole of a cache cleaned outright by `-cache` or `-modcache`; `-testcache` and `-fuzzcache` report nothing, as they only drop part of the build cache |
| `homebrew` | the size brew reports having freed | `brew cleanup --dry-run`, which deletes nothing and still reports what it would free |
| `git gc` | the size of `.git` before and after the repack | nothing - how much a repack saves is only knowable afterwards |
| `bazel clean` | the size of the workspace's output base | the same figure |
| `files and directories`, `explicit paths` | the size of what was deleted | the size of what would be deleted |

Two consequences of measuring docker with `docker system df`: a container
writing to disk while ddc runs eats into the figure, and an image freed by a
`--dry-run` is counted from the image list instead, which ignores the layers
it shares with an image that stays.

### Configuration

Here's what the config file looks like:

```yaml
# Default age, given to every rule that doesn't name one of its own,
# so a size-only rule can't take something you touched this morning.
# A rule's own age wins, "never" is left alone, and a rule written
# with no threshold at all counts as unset and takes this.
# Omit the key to leave size-only rules age-free.
age: 1m

# Directories to scan recursively for cleanup candidates.
root_paths:
  - ~/dev
  - ~/Movies

# Catch-all rule for any image not used by a container and not matched
# by a docker_image_prefix_include rule below. Without this, only images whose
# repository you have listed explicitly are ever considered, which tends
# to leave the long tail of one-off images untouched.
docker_unused_images: 500MB 3m

# Keep only the newest N versions of each image repository, deleting the
# rest. See "Keeping the latest versions" below for how versions are
# recognised.
docker_image_keep_latest: 2

# Docker image repository prefixes to remove. Any image whose
# repository starts with one of these prefixes will be deleted.
# Docker's dangling image prune runs automatically before this step.
# Optional size/age thresholds can be specified (same syntax as
# directories and files). Omit the threshold to delete all matches, or
# use "never" to pin the matches against every other rule.
# Where several prefixes match an image, the longest one wins.
docker_image_prefix_include:
  codingconcepts:
  cockroachdb: 6w
  golang: never

# Docker image repository prefixes to keep, whatever else the config
# says. An image whose repository starts with one of these is pinned
# against every other rule, including docker_image_keep_latest and the
# catch-all, and it wins even when a longer include prefix matches.
docker_image_prefix_exclude:
  - ghcr.io/home-assistant
  - keepme/

# Prune Docker build cache entries older than the given age.
# Uses an age threshold (e.g. "2w" for 2 weeks).
docker_build_cache: 2w

# Remove unused Docker volumes. Anonymous volumes (the ones docker
# creates for a container that did not name them) and named volumes are
# opted into separately, because a named volume is far more likely to
# hold data you meant to keep. The optional age applies to the volume's
# creation date.
#
# `docker volume prune` on its own only ever removes anonymous volumes,
# so `docker_volume_prune: true` is still accepted and means exactly
# that.
docker_volume_prune:
  anonymous: true
  named: false
  age: 1m

# Remove stopped containers.
docker_container_prune: true

# Remove networks not attached to any container.
docker_network_prune: true

# Clean files from the OS temp directory (os.TempDir()) using the
# same delete_files and delete_directories rules. Portable across
# platforms without hardcoding paths.
temp_cleanup: true

# Clean Bazel build outputs in detected workspaces. Workspaces are
# identified by the presence of WORKSPACE, WORKSPACE.bazel, or
# MODULE.bazel files. Reports reclaimed disk space from the output
# base. Skipped if bazel is not installed.
bazel_cleanup:
  clean: true           # run `bazel clean`
  expunge: false        # run `bazel clean --expunge` (removes entire output base)
  expunge_async: false  # run `bazel clean --expunge_async` (non-blocking, takes priority over expunge)

# Clean Go toolchain caches. Each sub-key maps to a `go clean` flag.
# Skipped if go is not installed.
go_cleanup:
  cache: true      # build cache (go clean -cache)
  modcache: true   # module cache (go clean -modcache)
  testcache: true  # test cache (go clean -testcache)
  fuzzcache: true  # fuzz cache (go clean -fuzzcache)

# Run `brew cleanup --prune=all` to remove old Homebrew downloads
# and caches. Skipped if brew is not installed.
homebrew_cleanup: true

# Run `git gc` on any git repository whose .git directory exceeds
# the given size threshold. The bare form `git_gc: 100MB` is still
# accepted and leaves pruning to git, which keeps unreachable
# objects for two weeks so that a bad reset or a dropped stash can
# still be recovered with `git fsck --lost-found`.
#
# prune sets how long an unreachable object survives: an age, or
# "now" to drop them immediately. Nearly all the space comes from
# the repack, not the prune, so "now" buys little and costs you the
# safety net.
git_gc:
  size: 100MB
  prune: 2w

# Delete branches that have been merged into the default branch
# (main/master) in every git repository found under root_paths.
git_clean_branches: true

# Directories to delete when they exceed the given thresholds.
# Each entry is a directory name mapped to a threshold string.
# Thresholds can specify a minimum size, a minimum age, or both:
#   "10MB"       - delete if the directory is at least 10 MB
#   "1y"         - delete if the directory is at least 1 year old
#   "10MB 1y"    - delete if at least 10 MB AND at least 1 year old
delete_directories:
  node_modules: 10MB
  .terraform/providers: 10MB 1y

# Files to delete by extension when they exceed the given thresholds.
# The same size and age threshold syntax applies.
delete_files:
  .log: 10MB 6m
  .csv: 300MB 1y
  .mp4: 50MB 1.5y

# Specific paths to delete when they exceed the given thresholds.
# Useful for well-known caches, package stores, and derived data
# directories. Supports ~ expansion.
delete_paths:
  ~/.cache/pip: 500MB 3m
  ~/.npm/_cacache: 500MB 3m
  ~/Library/Developer/Xcode/DerivedData: 1GB

# Directories to skip entirely during scanning. Matching is done
# by base name (e.g. ".git") or by trailing path segments
# (e.g. "Movies/TV" matches any path ending in Movies/TV).
ignore_directories:
  - .git
  - Movies/TV
```

#### Size units

| Unit | Value |
|------|-------|
| `B`  | Bytes |
| `KB` | Kilobytes (1,000 bytes) |
| `MB` | Megabytes (1,000,000 bytes) |
| `GB` | Gigabytes (1,000,000,000 bytes) |
| `TB` | Terabytes (1,000,000,000,000 bytes) |

#### Age units

| Unit | Value |
|------|-------|
| `d`  | Days |
| `w`  | Weeks |
| `m`  | Months (30 days) |
| `y`  | Years (365 days) |

Fractional values are supported (e.g. `1.5y`, `0.5MB`).

### Rule precedence

Most config keys are independent, but a few overlap. Where they do, these are
the rules.

#### Order of steps

| # | Step | Driven by | Skipped when |
|---|---|---|---|
| 1 | Docker | `docker_*` | docker isn't installed, or the daemon isn't running |
| 2 | Go caches | `go_cleanup` | `go` isn't on `PATH` |
| 3 | Homebrew | `homebrew_cleanup` | `brew` isn't on `PATH` |
| 4 | Filesystem walk | `root_paths`, `temp_cleanup` | no walk rule is set (see below) |
| 5 | Explicit paths | `delete_paths` | unset |

The docker step runs in this order: report disk usage, prune dangling images,
apply the image rules, prune stopped containers, prune build cache, prune
volumes, prune networks, measure what was reclaimed.

Whatever each step frees is collected and printed as a
[run summary](#run-summary) at the end, however the run ends - an interrupted
or failed run still accounts for what it had already freed.

Step 4 is skipped entirely unless at least one of `delete_directories`,
`delete_files`, `git_gc`, `git_clean_branches` or `bazel_cleanup` is set.
`temp_cleanup` only adds a root to walk, so on its own it does nothing.

#### Docker images

Applied from most to least specific; the first row that matches decides.

| # | Rule | Outcome |
|---|---|---|
| 1 | image backs a container, running or stopped | kept, always |
| 2 | matching prefix in `docker_image_prefix_exclude` | kept, always - this beats every rule below, however long a matching include prefix is |
| 3 | matching prefix in `docker_image_prefix_include` is `never` | kept, always - this also beats rule 4 |
| 4 | `docker_image_keep_latest` | superseded versions removed |
| 5 | longest matching prefix in `docker_image_prefix_include` | that prefix's threshold decides, and the catch-all is never consulted |
| 6 | `docker_unused_images` | applies only when no prefix matched at all |

Three consequences:

- An include rule shields its images from the catch-all even when its own
  threshold *isn't* met. Given `docker_unused_images: 500MB 3m` and
  `mysql: 6m`, a four-month-old 1.1GB `mysql` image is kept: the prefix
  matches first, and four months is short of six.
- An include threshold doesn't shield its images from
  `docker_image_keep_latest`, because that's a separate pass. Only `never`, an
  exclude prefix, or an in-use image stops it.
- `docker_image_prefix_exclude` is the blunt version of `never`: no threshold,
  and no competition on prefix length. `cockroachdb` in the exclude list pins
  `cockroachdb/cockroach:v26.3.0` even when `cockroachdb/cockroach` is in the
  include list.

Prefixes in both lists are matched against the whole `repository:tag` string, with no
awareness of path boundaries, so `node` also matches `nodered/node-red`. Write
`node:` or `nodered/` when you mean one of them.

#### Filesystem walk

Applied to each directory as the walk reaches it.

| # | Condition | Outcome |
|---|---|---|
| 1 | matches `ignore_directories` | skipped whole; nothing inside is examined |
| 2 | contains a Bazel workspace marker | `bazel clean` runs, then the walk continues inside |
| 3 | contains a `.git` directory | `git_gc` and `git_clean_branches` run, then the walk continues inside |
| 4 | name matches `delete_directories` and the threshold is met | deleted; the walk doesn't descend |
| 5 | name matches `delete_directories` and the threshold **isn't** met | kept, and the walk still doesn't descend, so `delete_files` never sees what's inside |
| 6 | no match | the walk descends and `delete_files` is applied by extension |

Row 5 is the one that surprises people. A `node_modules` under its size
threshold protects every `.log` and `.map` inside it.

#### What each rule is scoped to

| Rule | Confined to `root_paths` | Obeys `ignore_directories` |
|---|---|---|
| `delete_directories` | yes | yes |
| `delete_files` | yes | yes |
| `git_gc`, `git_clean_branches` | yes | yes |
| `bazel_cleanup` | yes | yes |
| `temp_cleanup` | adds `os.TempDir()` as a root | yes |
| `delete_paths` | no - absolute paths, wherever they are | **no** |
| `docker_*` | no | no |

So a path listed in both `delete_paths` and `ignore_directories` is still
deleted. `delete_paths` is the escape hatch, not part of the scan.

#### Thresholds

| Form | Meaning |
|---|---|
| `10MB` | size only |
| `1y` | age only |
| `10MB 1y` | both must hold - AND, not OR |
| empty | always matches, unless a top-level `age` is set, which it then takes |
| `never` | never matches, and pins the match against other rules (`docker_image_prefix_include` only) |

#### Flags

| Invocation | Behaviour |
|---|---|
| `ddc` | deletes everything the rules select, without prompting |
| `ddc --dry-run` | reports what would go, deletes nothing - the one command it still runs is `brew cleanup --dry-run`, which deletes nothing either and is the only way to put a figure on that step |
| `ddc --interactive` | prompts per item, deletes on `y` |
| `ddc --dry-run --interactive` | prompts per item but deletes nothing; declining also drops the item from the reported totals |

#### Colour

Output is colourised when stderr is a terminal: sizes in red, the thing being
acted on (path, image tag, volume name, branch) in yellow, and the `[dry-run]`
marker and the summary's rule in grey. Set `NO_COLOR` to turn it off, or `CLICOLOR_FORCE` to keep it
when piping.

### Docker images

Images are matched by ID rather than by tag, so an image carrying several tags
is only counted once. Removing some of an image's tags untags it without
freeing any space, and ddc reports that as `untagging` rather than `deleting`.

#### Image age

An image's age is taken from when it was tagged locally, which for a pulled
image is when you pulled it. This is deliberately not the image's creation
date, which belongs to whoever built the layers upstream and can be months
older than your copy - `golang: 3m` should not delete an image you pulled
yesterday from a release that was built in the spring.

#### Keeping the latest versions

Docker has no built-in way to say "keep the newest release of this image", so
`docker_image_keep_latest: N` implements it: for each repository, the newest N
versions are kept and the rest removed.

Only tags that start with a number are treated as versions, and they are
grouped by the variant that follows the number. Everything else is left alone.
That means:

| Tags in a repository | With `docker_image_keep_latest: 1` |
|---|---|
| `v26.3.0`, `v26.3.1`, `v26.3.5` | keeps `v26.3.5`, removes the other two |
| `3.20`, `3.21`, `latest` | keeps `3.21` and `latest`, removes `3.20` |
| `17`, `18.4-alpine3.23` | keeps both - different variants, not versions of one another |
| `apps`, `based`, `check`, `fixed` | keeps all - these name variants, not releases |

The last row is the reason for the restriction. Sorting tags by date instead
would quietly delete every branch build but the most recent one.

Each removal names the release that replaced it, both in the log and in the
`--interactive` prompt:

```
deleting image image=cockroachdb/cockroach:v26.3.0 age=1m reason=superseded superseded_by=cockroachdb/cockroach:v26.3.5 size=667.0MB

delete image alpine:3.20 (13.7MB, 3m), superseded by alpine:3.21? [y/N]
```

When an image carries several tags, only the last tag to go frees it, so only
that line carries a `size`. The others are untags.
