package runner

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/codingconcepts/ddc/pkg/config"
)

func TestParseVersionTag(t *testing.T) {
	cases := []struct {
		tag     string
		version []int
		variant string
		ok      bool
	}{
		{"v26.3.5", []int{26, 3, 5}, "", true},
		{"26.3.5", []int{26, 3, 5}, "", true},
		{"8.2.2", []int{8, 2, 2}, "", true},
		{"3.21", []int{3, 21}, "", true},
		{"17", []int{17}, "", true},
		{"18.4-alpine3.23", []int{18, 4}, "alpine3.23", true},
		{"7-alpine", []int{7}, "alpine", true},
		{"23.26.3.0-lite", []int{23, 26, 3, 0}, "lite", true},
		{"19.19.0.0", []int{19, 19, 0, 0}, "", true},
		{"1.2.3_rc1", []int{1, 2, 3}, "rc1", true},

		// Names rather than versions, which must never be treated as
		// superseded by a higher-numbered tag.
		{"latest", nil, "", false},
		{"pg17-latest", nil, "", false},
		{"sim-stages-dev", nil, "", false},
		{"check", nil, "", false},
		{"", nil, "", false},
		{"v", nil, "", false},
		{"-1.2", nil, "", false},
	}

	for _, c := range cases {
		version, variant, ok := parseVersionTag(c.tag)
		if ok != c.ok {
			t.Errorf("parseVersionTag(%q) ok = %v, want %v", c.tag, ok, c.ok)
			continue
		}

		if !ok {
			continue
		}

		if !reflect.DeepEqual(version, c.version) {
			t.Errorf("parseVersionTag(%q) version = %v, want %v", c.tag, version, c.version)
		}

		if variant != c.variant {
			t.Errorf("parseVersionTag(%q) variant = %q, want %q", c.tag, variant, c.variant)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b []int
		want int // sign only
	}{
		{[]int{26, 3, 5}, []int{26, 3, 1}, 1},
		{[]int{26, 3, 1}, []int{26, 3, 5}, -1},
		{[]int{26, 3}, []int{26, 3, 0}, 0},
		{[]int{26, 3, 1}, []int{26, 3}, 1},
		{[]int{3, 21}, []int{3, 9}, 1},
		{[]int{1}, []int{1}, 0},
	}

	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("compareVersions(%v, %v) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSplitRepoTag(t *testing.T) {
	cases := []struct{ image, repo, tag string }{
		{"cockroachdb/cockroach:v26.3.5", "cockroachdb/cockroach", "v26.3.5"},
		{"localhost:5000/cockroachdb/cockroach:v26.3.1", "localhost:5000/cockroachdb/cockroach", "v26.3.1"},
		{"alpine:latest", "alpine", "latest"},
		{"alpine", "alpine", ""},
	}

	for _, c := range cases {
		repo, tag := splitRepoTag(c.image)
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitRepoTag(%q) = (%q, %q), want (%q, %q)", c.image, repo, tag, c.repo, c.tag)
		}
	}
}

func TestSupersededTags(t *testing.T) {
	images := []dockerImage{
		// Three releases of one repository: only the newest survives.
		{id: "a", tags: []string{"cockroachdb/cockroach:v26.3.0"}},
		{id: "b", tags: []string{"cockroachdb/cockroach:v26.3.1"}},
		{id: "c", tags: []string{"cockroachdb/cockroach:v26.3.5"}},

		// Tags that name a variant rather than a release. A repository
		// tagged this way has no notion of "latest version", so none of
		// these may be removed.
		{id: "d", tags: []string{"demos:apps"}},
		{id: "e", tags: []string{"demos:based"}},
		{id: "f", tags: []string{"demos:check"}},

		// Different base images, not versions of one another.
		{id: "g", tags: []string{"postgres:17"}},
		{id: "h", tags: []string{"postgres:18.4-alpine3.23"}},

		// "latest" is not a version, so it is left alone even though the
		// numbered tags in the same repository are compared.
		{id: "i", tags: []string{"alpine:3.20"}},
		{id: "j", tags: []string{"alpine:3.21"}},
		{id: "k", tags: []string{"alpine:latest"}},
	}

	got := supersededTags(images, 1)

	// Each removal names the release that replaced it.
	want := map[string][]victim{
		"a": {{tag: "cockroachdb/cockroach:v26.3.0", reason: "superseded", supersededBy: "cockroachdb/cockroach:v26.3.5"}},
		"b": {{tag: "cockroachdb/cockroach:v26.3.1", reason: "superseded", supersededBy: "cockroachdb/cockroach:v26.3.5"}},
		"i": {{tag: "alpine:3.20", reason: "superseded", supersededBy: "alpine:3.21"}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("supersededTags() = %v, want %v", got, want)
	}
}

func TestSupersededTagsKeepsMoreThanOne(t *testing.T) {
	images := []dockerImage{
		{id: "a", tags: []string{"repo:1.0.0"}},
		{id: "b", tags: []string{"repo:2.0.0"}},
		{id: "c", tags: []string{"repo:3.0.0"}},
		{id: "d", tags: []string{"repo:4.0.0"}},
	}

	got := supersededTags(images, 2)

	var tags []string

	for _, victims := range got {
		for _, v := range victims {
			tags = append(tags, v.tag)

			// Even with two kept, the newest is what superseded them.
			if v.supersededBy != "repo:4.0.0" {
				t.Errorf("%s supersededBy = %q, want repo:4.0.0", v.tag, v.supersededBy)
			}
		}
	}

	sort.Strings(tags)

	want := []string{"repo:1.0.0", "repo:2.0.0"}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("supersededTags() tags = %v, want %v", tags, want)
	}
}

func TestSupersededTagsSharedImageID(t *testing.T) {
	// One image carrying both a version tag and "latest". The old release
	// should go; the shared image keeps its "latest" tag, so removal is an
	// untag rather than a delete.
	images := []dockerImage{
		{id: "old", tags: []string{"repo:1.0.0"}},
		{id: "new", tags: []string{"repo:2.0.0", "repo:latest"}},
	}

	got := supersededTags(images, 1)
	want := map[string][]victim{
		"old": {{tag: "repo:1.0.0", reason: "superseded", supersededBy: "repo:2.0.0"}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("supersededTags() = %v, want %v", got, want)
	}
}

func TestThresholdMet(t *testing.T) {
	day := 24 * time.Hour

	cases := []struct {
		name      string
		threshold *config.Threshold
		size      int64
		age       time.Duration
		want      bool
	}{
		{"nil matches everything", nil, 1, day, true},
		{"never matches nothing", &config.Threshold{Never: true}, 1_000_000_000, 365 * day, false},
		{"size met", &config.Threshold{Size: 1_000_000}, 2_000_000, 0, true},
		{"size not met", &config.Threshold{Size: 1_000_000}, 500_000, 0, false},
		{"age met", &config.Threshold{Age: 7 * day}, 0, 8 * day, true},
		{"age not met", &config.Threshold{Age: 7 * day}, 0, 6 * day, false},
		{"both met", &config.Threshold{Size: 1_000_000, Age: 7 * day}, 2_000_000, 8 * day, true},
		{"only size met", &config.Threshold{Size: 1_000_000, Age: 7 * day}, 2_000_000, 1 * day, false},
		{"only age met", &config.Threshold{Size: 1_000_000, Age: 7 * day}, 10, 8 * day, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := thresholdMet(c.threshold, c.size, c.age); got != c.want {
				t.Errorf("thresholdMet() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestImageRule(t *testing.T) {
	week := 7 * 24 * time.Hour
	catchAll := &config.Threshold{Age: 4 * week}
	golang := &config.Threshold{Age: week}
	never := &config.Threshold{Never: true}

	r := New(config.Config{
		DockerImagePrefixInclude: map[string]*config.Threshold{
			"golang":         golang,
			"golang:1.25":    never,
			"redpandadata":   nil,
			"cockroachdb":    {Age: 6 * week},
			"keepme/forever": never,
		},
		DockerUnusedImages: catchAll,
	}, true, false, false)

	cases := []struct {
		name    string
		tags    []string
		want    *config.Threshold
		matched bool
	}{
		{"longest prefix wins", []string{"golang:1.25.3"}, never, true},
		{"shorter prefix still matches", []string{"golang:1.24.0"}, golang, true},
		{"nil threshold means always", []string{"redpandadata/console:latest"}, nil, true},
		{"unmatched falls through to catch-all", []string{"container-registry.oracle.com/database/enterprise:19.19.0.0"}, catchAll, true},
		{"any tag can match", []string{"somethingelse:1", "golang:1.24"}, golang, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, matched := r.imageRule(dockerImage{tags: c.tags})
			if matched != c.matched {
				t.Fatalf("imageRule() matched = %v, want %v", matched, c.matched)
			}

			if got != c.want {
				t.Errorf("imageRule() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestImageRuleWithoutCatchAll(t *testing.T) {
	r := New(config.Config{
		DockerImagePrefixInclude: map[string]*config.Threshold{"golang": nil},
	}, true, false, false)

	if _, matched := r.imageRule(dockerImage{tags: []string{"redis:8"}}); matched {
		t.Error("imageRule() matched an image with no rule and no catch-all")
	}
}

func TestImagePrefixExclude(t *testing.T) {
	// An exclude wins over a longer include prefix and over the catch-all.
	r := New(config.Config{
		DockerImagePrefixInclude: map[string]*config.Threshold{
			"cockroachdb/cockroach": nil,
		},
		DockerImagePrefixExclude: []string{"cockroachdb", "keepme/"},
		DockerUnusedImages:       &config.Threshold{Age: time.Hour},
		DockerImageKeepLatest:    1,
	}, true, false, false)

	cases := []struct {
		name     string
		tags     []string
		excluded bool
	}{
		{"exclude beats a longer include prefix", []string{"cockroachdb/cockroach:v26.3.0"}, true},
		{"exclude beats the catch-all", []string{"keepme/thing:latest"}, true},
		{"any tag can trigger the exclude", []string{"redis:8", "keepme/thing:1"}, true},
		{"unrelated image is untouched", []string{"redis:8"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.imageExcluded(dockerImage{tags: c.tags}); got != c.excluded {
				t.Fatalf("imageExcluded() = %v, want %v", got, c.excluded)
			}

			// An excluded image has no rule at all, so nothing removes it.
			_, matched := r.imageRule(dockerImage{tags: c.tags})
			if c.excluded && matched {
				t.Error("imageRule() matched an excluded image")
			}

			if !c.excluded && !matched {
				t.Error("imageRule() should have fallen through to the catch-all")
			}
		})
	}
}

func TestDockerImageAge(t *testing.T) {
	created := time.Now().Add(-365 * 24 * time.Hour)
	tagged := time.Now().Add(-24 * time.Hour)

	// An image pulled yesterday but built a year ago is one day old, not one
	// year: the creation date belongs to whoever built it upstream.
	img := dockerImage{created: created, tagged: tagged}
	if age := img.age(); age > 48*time.Hour {
		t.Errorf("age() = %v, want roughly 1 day", age)
	}

	// With no local tag time recorded, fall back to the creation date.
	img = dockerImage{created: created}
	if age := img.age(); age < 300*24*time.Hour {
		t.Errorf("age() = %v, want roughly 1 year", age)
	}
}

func TestDockerVolumeAnonymous(t *testing.T) {
	cases := []struct {
		labels string
		want   bool
	}{
		{"com.docker.volume.anonymous=", true},
		{"com.docker.volume.anonymous", true},
		{"com.example.thing=1,com.docker.volume.anonymous=", true},
		{"com.docker.compose.project=home-ops", false},
		{"", false},
	}

	for _, c := range cases {
		v := dockerVolume{Labels: c.labels}
		if got := v.anonymous(); got != c.want {
			t.Errorf("anonymous() for labels %q = %v, want %v", c.labels, got, c.want)
		}
	}
}

func TestDockerVolumeUnused(t *testing.T) {
	cases := []struct {
		links string
		want  bool
	}{
		{"0", true},
		{"1", false},
		{"14", false},
		{"", false},
	}

	for _, c := range cases {
		v := dockerVolume{Links: c.links}
		if got := v.unused(); got != c.want {
			t.Errorf("unused() for links %q = %v, want %v", c.links, got, c.want)
		}
	}
}

func TestReclaimableSize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"29.46GB (91%)", "29.46GB"},
		{"44.51GB (98%)", "44.51GB"},
		{"0B", "0B"},
	}

	for _, c := range cases {
		if got := reclaimableSize(c.in); got != c.want {
			t.Errorf("reclaimableSize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNeverPinsAgainstKeepLatest(t *testing.T) {
	// A "never" rule has to win over the keep-latest pass too, otherwise
	// pinning a repository would still lose its older releases.
	r := New(config.Config{
		DockerImagePrefixInclude: map[string]*config.Threshold{"cockroachdb": {Never: true}},
		DockerImageKeepLatest:    1,
		DockerUnusedImages:       &config.Threshold{Age: time.Hour},
	}, true, false, false)

	img := dockerImage{id: "a", tags: []string{"cockroachdb/cockroach:v26.3.0"}}

	threshold, matched := r.imageRule(img)
	if !matched {
		t.Fatal("imageRule() should match the cockroachdb prefix")
	}

	if threshold == nil || !threshold.Never {
		t.Fatalf("imageRule() = %v, want a never threshold", threshold)
	}

	// The keep-latest pass alone still lists it, which is exactly why
	// cleanImages re-checks the rule before removing anything.
	superseded := supersededTags([]dockerImage{
		img,
		{id: "b", tags: []string{"cockroachdb/cockroach:v26.3.5"}},
	}, 1)
	if _, ok := superseded["a"]; !ok {
		t.Fatal("supersededTags() should list the older release")
	}
}

func TestVictimPrompt(t *testing.T) {
	img := dockerImage{size: 667_000_000, tagged: time.Now().Add(-60 * 24 * time.Hour)}

	superseded := victim{
		tag:          "cockroachdb/cockroach:v26.3.0",
		reason:       "superseded",
		supersededBy: "cockroachdb/cockroach:v26.3.5",
	}

	want := "delete image cockroachdb/cockroach:v26.3.0 (667.0MB, 2m), superseded by cockroachdb/cockroach:v26.3.5?"
	if got := superseded.prompt(img); got != want {
		t.Errorf("prompt() = %q, want %q", got, want)
	}

	unused := victim{tag: "chromadb/chroma:1.5.9", reason: "unused"}

	want = "delete image chromadb/chroma:1.5.9 (667.0MB, 2m)?"
	if got := unused.prompt(img); got != want {
		t.Errorf("prompt() = %q, want %q", got, want)
	}
}
