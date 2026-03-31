# ddc

Dev Deep Clean - give your machine a good clear out of everything it's accumulated during development.

ddc scans your filesystem and Docker installation to reclaim disk space by removing:

- Bloated directories like `node_modules` and `.terraform/providers`
- Old or oversized files matching extension-based rules
- Unused Docker images matching configured prefixes

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
        print commands without executing them
  -debug
        enable verbose debug logging
  -interactive
        prompt before each deletion
```

Run with the default config:

```sh
ddc
```

Preview what would be deleted without actually removing anything:

```sh
ddc --dry-run
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

### Configuration

Here's what the config file looks like:

```yaml
# Directories to scan recursively for cleanup candidates.
root_paths:
  - ~/dev
  - ~/Movies

# Docker image repository prefixes to remove. Any image whose
# repository starts with one of these prefixes will be deleted.
# Docker's dangling image prune runs automatically before this step.
# Optional size/age thresholds can be specified (same syntax as
# directories and files). Omit the threshold to delete all matches.
docker_image_prefixes:
  codingconcepts:
  cockroachdb: 6w

# Directories to delete when they exceed the given thresholds.
# Each entry is a directory name mapped to a threshold string.
# Thresholds can specify a minimum size, a minimum age, or both:
#   "10MB"       - delete if the directory is at least 10 MB
#   "1y"         - delete if the directory is at 1 year old
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
