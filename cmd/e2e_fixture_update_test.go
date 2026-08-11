package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpliceRegexFixture exercises the UPDATE_FIXTURES=true reconciliation logic (see
// spliceRegexFixture's doc comment and #833) against fixture-shaped inputs, without a live cluster:
// it's pure string manipulation, so these run unconditionally like TestE2ERegexFixturesAreAnchored.
func TestSpliceRegexFixture(t *testing.T) {
	// Modeled on the real tests/e2e-artifacts/vpa-workload-reverse-match.regex fixture: a Pod name
	// wildcard, a ReplicaSet name wildcard sharing its prefix with surrounding literal text, and two
	// numeric wildcards for a live-computed VPA recommendation.
	const vpaLike = `\A` + "\n" +
		`Deployment/vpa-burner -n e2e-vpa, created 1m ago` + "\n" +
		`  Pod/vpa-burner-[a-z0-9]+-[a-z0-9]+, created 1m ago Running, 1/1 ready` + "\n" +
		`  VerticalPodAutoscaler: burner: cpu=\d+m mem=\d+Mi` + "\n" +
		`  ReplicaSet "vpa-burner-[a-z0-9]+" has successfully progressed\.` + "\n" +
		`\z`

	t.Run("unchanged render round-trips byte-identical", func(t *testing.T) {
		stdout := "\n" +
			"Deployment/vpa-burner -n e2e-vpa, created 1m ago\n" +
			"  Pod/vpa-burner-abc12-xy789, created 1m ago Running, 1/1 ready\n" +
			"  VerticalPodAutoscaler: burner: cpu=127m mem=256Mi\n" +
			"  ReplicaSet \"vpa-burner-abc12\" has successfully progressed.\n"
		got, ok := spliceRegexFixture(vpaLike, stdout)
		require.True(t, ok)
		assert.Equal(t, vpaLike, got)
	})

	t.Run("literal change is spliced in, wildcards untouched", func(t *testing.T) {
		// Simulates a #829-style template change: a new literal clause appears mid-line, while the
		// wildcard-covered Pod name, CPU/mem recommendation, and ReplicaSet name all vary normally
		// between runs (different values than the "unchanged" case above).
		stdout := "\n" +
			"Deployment/vpa-burner -n e2e-vpa, created 1m ago\n" +
			"  Pod/vpa-burner-def45-zz111, created 1m ago Running, Ready:True, 1/1 ready\n" +
			"  VerticalPodAutoscaler: burner: cpu=64m mem=512Mi\n" +
			"  ReplicaSet \"vpa-burner-def45\" has successfully progressed.\n"
		got, ok := spliceRegexFixture(vpaLike, stdout)
		require.True(t, ok)

		// The new literal text made it in...
		assert.Contains(t, got, `Ready:True`)
		// ...while every wildcard span is byte-for-byte what it was before, not frozen to this
		// run's values.
		assert.Contains(t, got, `Pod/vpa-burner-[a-z0-9]+-[a-z0-9]+`)
		assert.Contains(t, got, `cpu=\d+m mem=\d+Mi`)
		assert.Contains(t, got, `ReplicaSet "vpa-burner-[a-z0-9]+"`)
		assert.NotContains(t, got, "64m")
		assert.NotContains(t, got, "def45")

		// And the regenerated fixture actually matches the render that produced it.
		re, err := regexp.Compile(`(?ms)` + got)
		require.NoError(t, err)
		assert.Regexp(t, re, stdout)
	})

	t.Run("wildcard itself no longer matching fails the splice", func(t *testing.T) {
		// cpu=<number>m no longer fits an output with a unit-less value -- not a literal-text
		// change, so this must be surfaced for a human, not guessed at.
		stdout := "\n" +
			"Deployment/vpa-burner -n e2e-vpa, created 1m ago\n" +
			"  Pod/vpa-burner-abc12-xy789, created 1m ago Running, 1/1 ready\n" +
			"  VerticalPodAutoscaler: burner: cpu=unknown mem=256Mi\n" +
			"  ReplicaSet \"vpa-burner-abc12\" has successfully progressed.\n"
		_, ok := spliceRegexFixture(vpaLike, stdout)
		assert.False(t, ok)
	})

	t.Run("no literal segments at all is never splice-able", func(t *testing.T) {
		_, ok := spliceRegexFixture(`\A\d+\z`, "42")
		assert.False(t, ok)
	})

	t.Run("a whole new line inserted next to a wildcard is spliced in", func(t *testing.T) {
		// Point 3 of #833's design: the new text isn't a wildcard-covered value, it's an entirely
		// new literal line, inserted between the Pod line's trailing wildcard and the next line.
		stdout := "\n" +
			"Deployment/vpa-burner -n e2e-vpa, created 1m ago\n" +
			"  Pod/vpa-burner-abc12-xy789, created 1m ago Running, 1/1 ready\n" +
			"  Ready:True\n" +
			"  VerticalPodAutoscaler: burner: cpu=127m mem=256Mi\n" +
			"  ReplicaSet \"vpa-burner-abc12\" has successfully progressed.\n"
		got, ok := spliceRegexFixture(vpaLike, stdout)
		require.True(t, ok)
		assert.Contains(t, got, "  Ready:True\n")
		assert.Contains(t, got, `cpu=\d+m mem=\d+Mi`)
		re, err := regexp.Compile(`(?ms)` + got)
		require.NoError(t, err)
		assert.Regexp(t, re, stdout)
	})
}

// TestParseFixtureSegmentsRoundTripsRealFixtures runs parseFixtureSegments over every real fixture
// under tests/e2e-artifacts/ and checks that re-escaping every literal segment (regexp.QuoteMeta)
// and rejoining it with the wildcard segments in order reproduces the original pattern byte-for-
// byte. It's a structural check independent of any live render: it doesn't confirm the *matching*
// (resolveFixtureSegments) is correct, but it does confirm the tokenizer handles every nesting/
// escaping shape actually present in the 116 real fixtures (deeply nested `(?:...)` groups,
// character classes containing literal parens, escaped literal specials, ...) without corrupting or
// misparsing any of them -- the load-bearing precondition the rest of the splice logic depends on.
func TestParseFixtureSegmentsRoundTripsRealFixtures(t *testing.T) {
	fixtures, err := filepath.Glob("../tests/e2e-artifacts/*.regex")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			content, err := os.ReadFile(fixture)
			require.NoError(t, err)
			pattern := string(content)
			segments := parseFixtureSegments(pattern)
			var rebuilt string
			for _, seg := range segments {
				if seg.literal {
					rebuilt += regexp.QuoteMeta(seg.text)
				} else {
					rebuilt += seg.text
				}
			}
			assert.Equal(t, pattern, rebuilt)
		})
	}
}

func TestParseFixtureSegments(t *testing.T) {
	segments := parseFixtureSegments(`\Afoo\.bar[a-z0-9]+baz\z`)
	require.Len(t, segments, 5)
	assert.Equal(t, fixtureSegment{literal: false, text: `\A`}, segments[0])
	assert.Equal(t, fixtureSegment{literal: true, text: `foo.bar`}, segments[1])
	assert.Equal(t, fixtureSegment{literal: false, text: `[a-z0-9]+`}, segments[2])
	assert.Equal(t, fixtureSegment{literal: true, text: `baz`}, segments[3])
	assert.Equal(t, fixtureSegment{literal: false, text: `\z`}, segments[4])
}
