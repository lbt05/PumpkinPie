package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/pumpkinpie/pumpkinpie/internal/agent/collector"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/docker"
	"github.com/pumpkinpie/pumpkinpie/internal/buildinfo"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Agent struct {
	masterAddr string
	nodeName   string
	machineID  string

	collector *collector.Collector
	docker    *docker.Client

	streamMu sync.Mutex
	stream   pb.AgentService_ConnectClient

	// containerPollInterval drives containerPollLoop. Set via the
	// agent's `container_poll_interval` YAML key. 0 disables polling
	// entirely (the events stream is the only sync path then).
	containerPollInterval time.Duration

	// dockerEventsEnabled gates dockerEventsLoop. Set via the
	// agent's `docker_events` YAML key. False skips the /events
	// subscription entirely — use on platforms where /events is
	// broken (Docker Desktop for Mac); the poll loop still
	// reconciles state.
	dockerEventsEnabled bool

	// reverseIDs maps docker container id -> master container id,
	// built up as containerPollLoop observes containers in ListAgent.
	// Used to emit action="destroy" events when a container vanishes
	// between polls (the events stream knows the mapping from the
	// label on the event, but ListAgent only gives us the docker id
	// at the moment we discover it's gone).
	reverseIDs map[string]string
}

// New wires up the gRPC client, Docker client and metrics collector.
// dockerSock is the resolved unix socket path; cmd/agent handles the
// YAML / DOCKER_SOCK / DOCKER_HOST / default precedence before
// passing the result in. containerPollInterval is the YAML string
// for the periodic ListAgent poll (empty = built-in default, "0s"
// disables). dockerEventsEnabled gates the /events subscription —
// false on platforms where /events is broken (Docker Desktop for
// Mac); the poll loop still reconciles state in that case.
func New(masterAddr, nodeName, machineID, dockerSock, containerPollInterval string, dockerEventsEnabled bool) (*Agent, error) {
	dc, err := docker.New(dockerSock)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	pollInterval, err := parseDuration(containerPollInterval, defaultContainerPollInterval)
	if err != nil {
		return nil, fmt.Errorf("container_poll_interval: %w", err)
	}
	return &Agent{
		masterAddr:            masterAddr,
		nodeName:              nodeName,
		machineID:             machineID,
		collector:             collector.New(nodeName),
		docker:                dc,
		containerPollInterval: pollInterval,
		dockerEventsEnabled:   dockerEventsEnabled,
	}, nil
}

// defaultContainerPollInterval is the periodic poll interval used
// when config leaves ContainerPollInterval empty. Long enough that a
// busy Docker host doesn't get hammered by N agents; short enough
// that out-of-band `docker stop` on the agent shows up in the UI
// within ~10s on platforms where Docker's /events stream is broken
// (Docker Desktop for Mac, certain embedded runtimes).
const defaultContainerPollInterval = 10 * time.Second

// parseDuration accepts the Go duration syntax ("10s", "1m30s")
// or an empty string (returns the default). Other forms — bare
// integers ("10"), junk input — fail loudly so a typo can't silently
// disable polling.
func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("must be non-negative, got %s", s)
	}
	return d, nil
}

// Run blocks until ctx is cancelled, reconnecting on failure with backoff.
func (a *Agent) Run(ctx context.Context) error {
	defer a.docker.Close()
	hostname, osStr, arch, err := collector.HostInfo()
	if err != nil {
		return fmt.Errorf("host info: %w", err)
	}
	version := normalizeAgentVersion(buildinfo.Version)

	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := a.serve(ctx, hostname, osStr, arch, version)
		if errors.Is(err, context.Canceled) {
			return err
		}
		log.Printf("agent disconnected: %v (reconnect in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		} else {
			backoff = 5 * time.Second
		}
	}
}

// normalizeAgentVersion strips the leading "v" from a semver tag so
// the value stored in the master's `nodes.agent_version` column is
// the bare version ("1.2.3") rather than the git tag form ("v1.2.3").
// The web UI's "agent v{{ ... }}" template re-adds the prefix on
// display, keeping the API contract stable for any external scripts
// that query agent_version directly.
func normalizeAgentVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}

func (a *Agent) serve(ctx context.Context, hostname, os, arch, version string) error {
	conn, err := grpc.NewClient(a.masterAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	cli := pb.NewAgentServiceClient(conn)

	stream, err := cli.Connect(ctx)
	if err != nil {
		return err
	}
	a.streamMu.Lock()
	a.stream = stream
	a.streamMu.Unlock()

	// 1) send register
	if err := stream.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_Register{
			Register: &pb.RegisterRequest{
				Name:         a.nodeName,
				Hostname:     hostname,
				Os:           os,
				Arch:         arch,
				AgentVersion: version,
				MachineId:    a.machineID,
			},
		},
	}); err != nil {
		return err
	}

	// 2) wait for register response
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	regResp := first.GetRegisterResp()
	if regResp == nil {
		return fmt.Errorf("expected RegisterResponse, got %T", first.Payload)
	}
	if !regResp.Ok {
		return fmt.Errorf("registration rejected: %s", regResp.Error)
	}
	log.Printf("registered as node %s (metrics interval %ds)", regResp.NodeId, regResp.MetricsIntervalSec)
	interval := time.Duration(regResp.MetricsIntervalSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	// 3) start metrics ticker
	metricsCtx, cancelMetrics := context.WithCancel(ctx)
	defer cancelMetrics()
	go a.metricsLoop(metricsCtx, interval)

	// 3a) start docker events watcher. Runs in parallel with the
	// metrics loop and the master-message read loop; all three are
	// driven by the same ctx so a stream failure just reconnects
	// within this session (the outer loop reconnects the whole
	// gRPC stream).
	//
	// Skipped when `docker_events: false` — useful on platforms
	// where /events is broken (Docker Desktop for Mac historically
	// closes the long-poll connection immediately) so we don't
	// burn CPU retrying it every second. The poll loop below still
	// keeps master state reconciled.
	if a.dockerEventsEnabled {
		eventsCtx, cancelEvents := context.WithCancel(ctx)
		defer cancelEvents()
		go a.dockerEventsLoop(eventsCtx)
	} else {
		log.Printf("docker events: disabled by config; relying on container poll only")
	}

	// 3b) start container-state poll loop. Companion to the events
	// stream: pushes the same ContainerStateChanged message type,
	// so the master sees one unified stream. Events gives sub-second
	// latency when supported; poll catches everything events misses
	// (Docker Desktop for Mac has long-standing /events issues, some
	// embedded container runtimes don't expose it at all). Set
	// `container_poll_interval: 0s` in the YAML to disable poll.
	if a.containerPollInterval > 0 {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go a.containerPollLoop(pollCtx, a.containerPollInterval)
	}

	// 4) read master messages until disconnect
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		a.handleMasterMessage(ctx, msg)
	}
}

func (a *Agent) send(msg *pb.AgentMessage) error {
	a.streamMu.Lock()
	s := a.stream
	a.streamMu.Unlock()
	if s == nil {
		return errors.New("no active stream")
	}
	return s.Send(msg)
}

func (a *Agent) metricsLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := a.collector.Collect(ctx)
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_Metrics{Metrics: r},
			}); err != nil {
				log.Printf("send metric: %v", err)
				return
			}
		}
	}
}

// dockerEventsLoop subscribes to Docker's /events stream and pushes
// a ContainerStateChanged message to the master every time one of our
// managed containers transitions lifecycle state — including changes
// we did NOT make ourselves (the user ran `docker stop` on the host,
// the container crashed, the kernel OOM-killed it, ...). Without
// this, the master's stored state would diverge from Docker's truth
// the moment anyone touches the container out-of-band.
//
// Caveat: Docker's /events stream is famously flaky on Docker Desktop
// for Mac (closes immediately) and absent on some embedded container
// runtimes. containerPollLoop is the always-on fallback that catches
// anything events misses. The two paths coexist — events pushes
// faster, poll pushes eventually — and the master's last-write-wins
// semantics make them composable.
//
// Reconnect: Docker closes the long-poll connection after ~5 min of
// idle (and immediately on daemon restart). On any break we open a
// new stream with `since=lastSeen` so we replay everything we missed
// after a successful send. The agent does not advance lastSeen for
// unsent events, so they replay on the next reconnect.
//
// Log noise: if the daemon closes the stream immediately (the
// Docker-for-Mac symptom), the naive loop logs "reconnecting" every
// second. After fastFailsBeforeQuiet consecutive fast failures we
// quiet down to once per minute — the polling loop is doing the real
// work anyway, and a flood of log lines would mask genuine problems.
func (a *Agent) dockerEventsLoop(ctx context.Context) {
	const (
		fastFailWindow       = 30 * time.Second
		fastFailsBeforeQuiet = 3
		quietBackoff         = time.Minute
	)
	backoff := time.Second
	var lastSeen time.Time
	var recentFails []time.Time
	quiet := false
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ch, err := a.docker.Events(ctx, lastSeen)
		if err != nil {
			a.noteEventsFailure(&recentFails, fastFailWindow, fastFailsBeforeQuiet, &quiet)
			if quiet {
				log.Printf("docker events: subscribe failed: %v (events unavailable on this platform; polling will reconcile state)", err)
			} else {
				log.Printf("docker events: subscribe failed: %v (retry in %s)", err, backoff)
			}
			if !sleep(ctx, backoffFor(backoff, quiet, quietBackoff)) {
				return
			}
			continue
		}
		backoff = time.Second
		recentFails = recentFails[:0]
		quiet = false
		for ev := range ch {
			cid, _ := ev.Attributes["pumpkinpie.container_id"]
			if cid == "" {
				continue
			}
			state, status := mapDockerActionToState(ev.Action)
			if state == "" {
				continue
			}
			msg := &pb.ContainerStateChanged{
				ContainerId: cid,
				DockerId:    ev.ID,
				Action:      ev.Action,
				State:       state,
				Status:      status,
				TsUnixMs:    ev.Time.UnixMilli(),
			}
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_ContainerStateChanged{ContainerStateChanged: msg},
			}); err != nil {
				log.Printf("docker events: send state changed for %s: %v", shortID(ev.ID), err)
				// Don't advance lastSeen — on next reconnect we'll
				// replay this event so the master doesn't miss it.
				return
			}
			if !ev.Time.IsZero() {
				lastSeen = ev.Time
			}
		}
		if err := ctx.Err(); err != nil {
			return
		}
		a.noteEventsFailure(&recentFails, fastFailWindow, fastFailsBeforeQuiet, &quiet)
		if !quiet {
			log.Printf("docker events: stream closed, reconnecting")
		}
	}
}

// noteEventsFailure trims recentFails to the configured window and
// flips *quiet to true once fastFailsBeforeQuiet failures land inside
// it. Once quiet, subsequent failures won't spam the log — the agent
// just keeps retrying at a slower cadence.
func (a *Agent) noteEventsFailure(recentFails *[]time.Time, window time.Duration, threshold int, quiet *bool) {
	now := time.Now()
	cutoff := now.Add(-window)
	pruned := (*recentFails)[:0]
	for _, t := range *recentFails {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	pruned = append(pruned, now)
	*recentFails = pruned
	if len(pruned) >= threshold {
		*quiet = true
	}
}

// backoffFor returns the next reconnect delay. Linear growth capped
// at 30s when events work normally; longer (quietBackoff) once we've
// concluded the platform doesn't support /events, so we don't burn
// CPU retrying every second.
func backoffFor(cur time.Duration, quiet bool, quietBackoff time.Duration) time.Duration {
	if quiet {
		return quietBackoff
	}
	next := cur * 2
	if next > 30*time.Second {
		next = 30 * time.Second
	}
	return next
}

// sleep returns false if ctx was cancelled during the wait.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// containerPollLoop is the always-on fallback that catches every
// out-of-band container state change Docker sees — used as the
// primary signal on platforms where /events doesn't work, and as a
// drift-correction safety net on platforms where it does. Polling
// can't beat events on latency (bounded by `interval`) but works
// everywhere /containers/json does, which is every Docker engine.
//
// State cache: we remember the last state we pushed for each docker
// id and only send a message when it changes. Without the cache the
// master would receive a redundant update every `interval` for every
// running container.
//
// Destroy detection: a container that disappears from ListAgent
// (e.g. user ran `docker rm`) is reported as action="destroy" so
// the master can free its GPU reservation, mirroring what the
// events stream would have done.
func (a *Agent) containerPollLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	lastState := map[string]string{} // dockerID -> last pushed master-state
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		infos, err := a.docker.ListAgent(ctx)
		if err != nil {
			log.Printf("container poll: list: %v", err)
			continue
		}
		seen := make(map[string]struct{}, len(infos))
		for _, info := range infos {
			if info.DockerId == "" || info.ContainerId == "" {
				continue
			}
			seen[info.DockerId] = struct{}{}
			if a.reverseIDs == nil {
				a.reverseIDs = map[string]string{}
			}
			a.reverseIDs[info.DockerId] = info.ContainerId
			state, status := mapDockerListState(info.State, info.Status)
			if state == "" {
				continue
			}
			if prev, ok := lastState[info.DockerId]; ok && prev == state {
				// No change — don't bother the master. The first
				// poll after agent start always pushes (no entry)
				// so the master picks up the initial state.
				continue
			}
			lastState[info.DockerId] = state
			msg := &pb.ContainerStateChanged{
				ContainerId: info.ContainerId,
				DockerId:    info.DockerId,
				Action:      "poll",
				State:       state,
				Status:      status,
				TsUnixMs:    time.Now().UnixMilli(),
			}
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_ContainerStateChanged{ContainerStateChanged: msg},
			}); err != nil {
				log.Printf("container poll: send: %v", err)
				delete(lastState, info.DockerId)
				return
			}
		}
		// Containers we knew about last tick but don't see now have
		// been destroyed (out-of-band `docker rm` or auto-removed
		// because they were started with --rm). Mirror what the
		// events stream does on action=destroy so the master frees
		// the GPU reservation.
		for dockerID, prev := range lastState {
			if _, ok := seen[dockerID]; ok {
				continue
			}
			if prev == "exited" || prev == "removed" {
				// Already terminal — nothing to free, just drop.
				delete(lastState, dockerID)
				continue
			}
			// We don't know the master container_id for the vanished
			// dockerID — look it up by remembering the reverse map
			// alongside lastState. (See containerReverseID below.)
			cid, ok := a.containerReverseID(dockerID)
			if !ok {
				delete(lastState, dockerID)
				continue
			}
			delete(lastState, dockerID)
			msg := &pb.ContainerStateChanged{
				ContainerId: cid,
				DockerId:    dockerID,
				Action:      "destroy",
				State:       "exited",
				Status:      "exited",
				TsUnixMs:    time.Now().UnixMilli(),
			}
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_ContainerStateChanged{ContainerStateChanged: msg},
			}); err != nil {
				log.Printf("container poll: send destroy: %v", err)
				return
			}
		}
	}
}

// containerReverseID returns the master container_id for a known
// docker container id. The poll loop populates this map as it sees
// containers in ListAgent (which carries both ids).
func (a *Agent) containerReverseID(dockerID string) (string, bool) {
	if a.reverseIDs == nil {
		return "", false
	}
	cid, ok := a.reverseIDs[dockerID]
	return cid, ok
}

// mapDockerActionToState folds Docker's event-action vocabulary onto
// the master's state string. Unknown actions return ("", "") so the
// agent doesn't churn master state with harmless transitions like
// exec_create/attach.
func mapDockerActionToState(action string) (state, status string) {
	switch action {
	case "start":
		return "running", "running"
	case "unpause":
		return "running", "running"
	case "restart":
		return "running", "running"
	case "die":
		return "exited", "exited"
	case "stop":
		return "exited", "exited"
	case "kill":
		return "exited", "killed"
	case "pause":
		return "paused", "paused"
	case "destroy":
		return "exited", "exited"
	default:
		return "", ""
	}
}

// mapDockerListState maps the State/Status fields from
// /containers/json onto the master's vocabulary. Docker's State is
// already coarse ("running", "exited", "paused", "restarting",
// "dead", "created", "removing") so this mostly just normalises the
// master-facing status string.
func mapDockerListState(state, status string) (string, string) {
	switch state {
	case "running":
		return "running", "running"
	case "exited":
		return "exited", "exited"
	case "paused":
		return "paused", "paused"
	case "restarting":
		return "running", "restarting"
	case "dead":
		return "exited", "dead"
	case "created":
		// Created but never started — treat as a non-running state
		// the UI can distinguish ("Up 0 seconds" wouldn't fit).
		return "exited", "created"
	case "removing":
		return "exited", "removing"
	default:
		return "", ""
	}
}

func (a *Agent) handleMasterMessage(ctx context.Context, msg *pb.MasterMessage) {
	switch p := msg.Payload.(type) {
	case *pb.MasterMessage_CreateContainer:
		go a.handleCreate(ctx, p.CreateContainer)
	case *pb.MasterMessage_StopContainer:
		go a.handleStop(ctx, p.StopContainer)
	case *pb.MasterMessage_StartContainer:
		go a.handleStart(ctx, p.StartContainer)
	}
}

func (a *Agent) handleCreate(ctx context.Context, cmd *pb.CreateContainerCommand) {
	dockerID, err := a.docker.Create(ctx, cmd)
	resp := &pb.ContainerCreated{ContainerId: cmd.ContainerId, DockerId: dockerID}
	if err != nil {
		resp.Error = err.Error()
		log.Printf("create container %s failed: %v", cmd.ContainerId, err)
	} else {
		log.Printf("created container %s (docker %s) on %s", cmd.ContainerId, shortID(dockerID), a.nodeName)
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerCreated{ContainerCreated: resp}})
}

func (a *Agent) handleStop(ctx context.Context, cmd *pb.StopContainerCommand) {
	err := a.docker.Stop(ctx, cmd.DockerId, cmd.Remove)
	resp := &pb.ContainerStopped{ContainerId: cmd.ContainerId, DockerId: cmd.DockerId}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerStopped{ContainerStopped: resp}})
}

func (a *Agent) handleStart(ctx context.Context, cmd *pb.StartContainerCommand) {
	err := a.docker.Start(ctx, cmd.DockerId)
	resp := &pb.ContainerStarted{ContainerId: cmd.ContainerId, DockerId: cmd.DockerId}
	if err != nil {
		resp.Error = err.Error()
		log.Printf("start container %s failed: %v", cmd.ContainerId, err)
	} else {
		log.Printf("started container %s (docker %s) on %s", cmd.ContainerId, shortID(cmd.DockerId), a.nodeName)
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerStarted{ContainerStarted: resp}})
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
