package agent

import (
	"testing"
	"time"
)

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

// TestMapDockerActionToState locks down the lifecycle-action → master-state
// translation used by dockerEventsLoop. The master doesn't speak Docker's
// action vocabulary; if this map changes silently, out-of-band `docker stop`
// (etc.) will stop showing up in the UI as "exited".
func TestMapDockerActionToState(t *testing.T) {
	cases := []struct {
		action    string
		wantState string
		wantStat  string
	}{
		{"start", "running", "running"},
		{"unpause", "running", "running"},
		{"restart", "running", "running"},
		{"die", "exited", "exited"},
		{"stop", "exited", "exited"},
		{"kill", "exited", "killed"},
		{"pause", "paused", "paused"},
		{"destroy", "exited", "exited"},

		// Harmless transitions the agent must ignore so they don't
		// churn master state.
		{"exec_create", "", ""},
		{"exec_start", "", ""},
		{"exec_die", "", ""},
		{"attach", "", ""},
		{"", "", ""},
		// Unknown future action: must default to ignore rather than
		// overwriting the master's stored state with garbage.
		{"some_future_action", "", ""},
	}
	for _, tc := range cases {
		gotState, gotStat := mapDockerActionToState(tc.action)
		if gotState != tc.wantState || gotStat != tc.wantStat {
			t.Errorf("mapDockerActionToState(%q) = (%q, %q), want (%q, %q)",
				tc.action, gotState, gotStat, tc.wantState, tc.wantStat)
		}
	}
}

// TestMapDockerListState covers the periodic-poll path (the fallback
// for platforms where /events doesn't work). Inputs mirror what
// /containers/json emits.
func TestMapDockerListState(t *testing.T) {
	cases := []struct {
		dockerState, dockerStatus string
		wantState, wantStatus     string
	}{
		{"running", "Up 5 minutes", "running", "running"},
		{"exited", "Exited (0) 30 seconds ago", "exited", "exited"},
		{"paused", "Up 10 minutes (Paused)", "paused", "paused"},
		{"restarting", "Restarting (1) 5 seconds ago", "running", "restarting"},
		{"dead", "Dead", "exited", "dead"},
		{"created", "Created", "exited", "created"},
		{"removing", "Removal In Progress", "exited", "removing"},
		{"unknown", "", "", ""},
	}
	for _, tc := range cases {
		gotState, gotStat := mapDockerListState(tc.dockerState, tc.dockerStatus)
		if gotState != tc.wantState || gotStat != tc.wantStatus {
			t.Errorf("mapDockerListState(%q, %q) = (%q, %q), want (%q, %q)",
				tc.dockerState, tc.dockerStatus,
				gotState, gotStat, tc.wantState, tc.wantStatus)
		}
	}
}

// TestParseDuration exercises the container_poll_interval parser —
// the YAML field accepts Go duration syntax or empty (default). Bare
// integers must fail so a typo can't silently disable polling.
func TestParseDuration(t *testing.T) {
	const def = 10 * time.Second
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", def, false},
		{"10s", 10 * time.Second, false},
		{"1m30s", 90 * time.Second, false},
		// "0s" explicitly disables polling — must be accepted.
		{"0s", 0, false},
		// Bare integer / unit-less junk must fail so a typo can't
		// silently disable polling.
		{"ten seconds", 0, true},
		{"abc", 0, true},
		// Negative durations are nonsensical and must fail.
		{"-5s", 0, true},
	}
	for _, tc := range cases {
		got, err := parseDuration(tc.in, def)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDuration(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDuration(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNoteEventsFailure locks down the platform-detection heuristic.
// The Docker-for-Mac symptom is "stream closes immediately"; we treat
// 3 failures inside a 30s window as a signal that /events doesn't
// work on this platform and stop spamming the log every second.
func TestNoteEventsFailure(t *testing.T) {
	const window = 30 * time.Second
	const threshold = 3

	t.Run("below threshold stays loud", func(t *testing.T) {
		fails := []time.Time{}
		quiet := false
		a := &Agent{}
		a.noteEventsFailure(&fails, window, threshold, &quiet)
		a.noteEventsFailure(&fails, window, threshold, &quiet)
		if quiet {
			t.Errorf("expected loud after 2 failures, got quiet=true")
		}
	})

	t.Run("hitting threshold quiets down", func(t *testing.T) {
		fails := []time.Time{}
		quiet := false
		a := &Agent{}
		for i := 0; i < threshold; i++ {
			a.noteEventsFailure(&fails, window, threshold, &quiet)
		}
		if !quiet {
			t.Errorf("expected quiet after %d failures, got quiet=false", threshold)
		}
	})

	t.Run("old failures fall out of window", func(t *testing.T) {
		now := time.Now()
		fails := []time.Time{now.Add(-time.Minute), now.Add(-time.Minute)}
		quiet := false
		a := &Agent{}
		a.noteEventsFailure(&fails, window, threshold, &quiet)
		if quiet {
			t.Errorf("expected stale failures to be pruned before threshold check")
		}
	})
}
