// Package config loads the agent's YAML configuration file.
//
// One file (`pp-agent.yaml` by default, override with `--config`)
// holds every knob the agent exposes — master gRPC address, node
// name, persistent state directory, and the Docker socket path.
// The schema mirrors what the operator would previously have passed
// as CLI flags or env vars; see pp-agent.yaml in the repo for the
// canonical form.
//
// Loading rules mirror the master config package:
//   - A missing file is not an error — defaults are returned.
//   - A malformed file is an error (caught at startup).
//   - Blank fields keep their built-in defaults, so partial YAMLs
//     work as well as the full file.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the canonical config location used when --config
// is not provided. Co-located with the master's /etc/pp/pp-master.yaml
// so operators have one etc dir to remember.
const DefaultPath = "/etc/pp/pp-agent.yaml"

// DefaultMaster is what the agent dials when no override is set.
// Matches the default in the master's contrib systemd unit.
const DefaultMaster = "pp-master.internal:7000"

// Config is the agent process's full configuration.
type Config struct {
	// Master is the gRPC address the agent dials to register itself.
	// Format: "host:port".
	Master string `yaml:"master"`
	// Name is the human-readable node name shown in the dashboard.
	// Empty = os.Hostname() at startup.
	Name string `yaml:"name"`
	// StateDir holds the machine-id file. Empty = the runner applies
	// a platform-aware default (/var/lib/pp-agent on Linux,
	// $XDG_STATE_HOME/pumpkinpie / ~/.local/state/pumpkinpie
	// elsewhere). See applyDefaults for why we don't pre-fill it.
	StateDir string `yaml:"state_dir"`
	// DockerSock is the path to the Docker daemon socket. The runner
	// applies env-var fallback (DOCKER_SOCK / DOCKER_HOST) on top of
	// this value so ad-hoc CLI use still works.
	DockerSock string `yaml:"docker_sock"`
	// ContainerPollInterval is how often the agent reconciles the
	// master's view of each container against Docker's true state by
	// calling /containers/json. Runs alongside Docker's /events
	// stream — events gives sub-second updates when supported, poll
	// gives correctness on platforms where /events is broken
	// (Docker Desktop for Mac, some embedded runtimes). Format is
	// Go duration syntax ("10s", "1m"). Empty = built-in default
	// (10s). "0s" disables polling, leaving events as the only path.
	ContainerPollInterval string `yaml:"container_poll_interval"`
	// DockerEvents controls whether the agent subscribes to Docker's
	// /events stream. Pointer so we can distinguish "unset" (use the
	// built-in default of true) from an explicit false — bool's zero
	// value would force every existing config to opt out. Set to
	// false on platforms where /events is broken (Docker Desktop for
	// Mac historically closes the long-poll connection immediately)
	// to skip the failing subscribe entirely; the polling fallback
	// still reconciles state.
	DockerEvents *bool `yaml:"docker_events"`
}

// defaults returns a Config populated with the same values the
// previous flag-based version used, so behaviour stays identical for
// operators who never created a YAML. StateDir and DockerSock are
// intentionally left blank here — see applyDefaults for why.
func defaults() Config {
	return Config{
		Master: DefaultMaster,
		Name:   "",
	}
}

// Load reads and parses the YAML at `path`. If path is empty, the
// DefaultPath is used. A missing file is not an error — defaults are
// returned — but a malformed file is.
//
// The second return value reports whether the file existed, which
// the caller uses to decide between "loaded from <path>" and
// "no config file, using defaults" in its startup log line.
func Load(path string) (Config, bool, error) {
	if path == "" {
		path = DefaultPath
	}
	cfg := defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, true, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	return cfg, true, nil
}

// applyDefaults fills Master with its built-in default when blank.
// Name, StateDir, and DockerSock are intentionally left blank:
//   - Name: the runner resolves it from os.Hostname() so the agent
//     picks up the real hostname dynamically.
//   - StateDir: the runner applies a platform-aware default
//     (/var/lib/pp-agent on Linux, $XDG_STATE_HOME/pumpkinpie /
//     ~/.local/state/pumpkinpie elsewhere). Hard-coding
//     /var/lib/pp-agent here would make ./bin/pp agent unusable
//     on macOS / non-root dev hosts where that path isn't writable.
//   - DockerSock: empty tells the runner to fall through the
//     env-var chain ($DOCKER_SOCK / DOCKER_HOST) before defaulting
//     to /var/run/docker.sock. Filling it in would silently disable
//     that fallback for partial YAMLs.
func (c *Config) applyDefaults() {
	if c.Master == "" {
		c.Master = DefaultMaster
	}
}

// DockerEventsEnabled returns the effective value of DockerEvents,
// defaulting to true when the YAML left it unset.
func (c *Config) DockerEventsEnabled() bool {
	if c.DockerEvents == nil {
		return true
	}
	return *c.DockerEvents
}