package agent

import "testing"

func TestNormalizeAgentVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// release tags as produced by goreleaser's `{{ .Version }}`
		// template — retains the "v" prefix.
		{"v0.1.0", "0.1.0"},
		{"v1.2.3-rc1", "1.2.3-rc1"},
		{"v2.0.0-beta.2", "2.0.0-beta.2"},

		// dev / unknown already have no prefix — must pass through.
		{"dev", "dev"},
		{"unknown", "unknown"},
		{"", ""},

		// Pre-release builds with ldflags overriding the version
		// (e.g. `make build VERSION=v1.2.3`) keep the trim behavior.
		{"v9.9.9", "9.9.9"},
	}
	for _, tc := range cases {
		if got := normalizeAgentVersion(tc.in); got != tc.want {
			t.Errorf("normalizeAgentVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
