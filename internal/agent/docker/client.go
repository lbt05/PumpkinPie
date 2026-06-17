// Package docker provides a minimal client for the Docker Engine HTTP API,
// avoiding the upstream SDK churn. The agent needs only:
//   - create + start a container
//   - stop + remove a container
//   - inspect to find the host port Docker mapped for a container port
//   - list containers
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Client struct {
	host   string // e.g. unix:///var/run/docker.sock
	http   *http.Client
	apiVer string
}

// New creates a Docker Engine client targeting the unix socket at
// sockPath. The socket resolution (config file vs. DOCKER_SOCK /
// DOCKER_HOST env vars vs. built-in default) lives in cmd/agent so
// this package stays free of os.Getenv calls and easy to test.
func New(sockPath string) (*Client, error) {
	c := &Client{host: "unix://" + sockPath, apiVer: "v1.43"}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sockPath)
		},
		MaxIdleConns:        16,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	c.http = &http.Client{Transport: tr, Timeout: 5 * time.Minute}
	return c, nil
}

func (c *Client) Close() error { return nil }

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "/_ping", nil)
	return err
}

// ImageExists returns true if the local Docker daemon has the given image
// reference in its cache. Used to decide whether a pull is needed under
// the "IfNotPresent" pull policy.
func (c *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	name := urlPathEscape(image)
	_, err := c.do(ctx, "GET", "/images/"+name+"/json", nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), " -> 404") {
		return false, nil
	}
	return false, err
}

// Create starts a container. Returns the docker container ID.
//
// Pull policy:
//   cmd.Pull == true  -> Always: pull unconditionally (also updates the
//     local tag if a newer version exists upstream).
//   cmd.Pull == false -> IfNotPresent: only pull if the image is not
//     already in the local cache. Most common case.
func (c *Client) Create(ctx context.Context, cmd *pb.CreateContainerCommand) (string, error) {
	if cmd.Pull {
		if err := c.pull(ctx, cmd.Image); err != nil {
			return "", fmt.Errorf("pull %s: %w", cmd.Image, err)
		}
	} else {
		exists, err := c.ImageExists(ctx, cmd.Image)
		if err != nil {
			// non-fatal: just try the create, the daemon will tell us
			log.Printf("image-exists check for %s failed: %v (proceeding with create)", cmd.Image, err)
		} else if !exists {
			log.Printf("image %s not present locally, pulling", cmd.Image)
			if err := c.pull(ctx, cmd.Image); err != nil {
				return "", fmt.Errorf("pull %s (IfNotPresent): %w", cmd.Image, err)
			}
		}
	}

	exposed := map[string]struct{}{}
	portBindings := map[string][]map[string]string{}
	for _, p := range cmd.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key := fmt.Sprintf("%d/%s", p.ContainerPort, proto)
		exposed[key] = struct{}{}
		// HostPort is the port Docker binds on the agent host. The master
		// defaults it to ContainerPort when the user didn't specify one
		// (mirrors `docker run -p X:X`), so 0 here means a legacy caller
		// that pre-dates the field — fall back to Docker picking an
		// ephemeral port so we never silently bind 0 on the host.
		hostPort := p.HostPort
		if hostPort == 0 {
			hostPort = p.ContainerPort
		}
		// Omit HostIp so Docker binds on 0.0.0.0 — mirrors the
		// `docker run -p X:X` shorthand and lets external clients
		// reach the published port. Security (token / firewall /
		// reverse proxy) is the caller's responsibility.
		portBindings[key] = []map[string]string{{"HostPort": fmt.Sprintf("%d", hostPort)}}
	}

	body := map[string]any{
		"Image":        cmd.Image,
		"Env":          cmd.Env,
		"Cmd":          cmd.Cmd,
		"ExposedPorts": exposed,
		"HostConfig": map[string]any{
			"PortBindings": portBindings,
			"AutoRemove":   false,
			"Binds":        cmd.VolumeBinds,
		},
		"Labels": map[string]string{
			"pumpkinpie.container_id": cmd.ContainerId,
			"pumpkinpie.managed":      "true",
		},
	}
	if cmd.Name != "" {
		body["name"] = cmd.Name
	}
	if cmd.Resources != nil {
		host := body["HostConfig"].(map[string]any)
		if cmd.Resources.CpuCores > 0 {
			host["CpuQuota"] = int64(cmd.Resources.CpuCores * 100000.0)
			host["CpuPeriod"] = 100000
		}
		if cmd.Resources.MemoryBytes > 0 {
			host["Memory"] = int64(cmd.Resources.MemoryBytes)
		}
		if cmd.Resources.GpuCount > 0 {
			req := map[string]any{
				"Capabilities": [][]string{{"gpu"}},
			}
			if len(cmd.Resources.GpuDeviceIds) > 0 {
				ids := make([]string, len(cmd.Resources.GpuDeviceIds))
				for i, idx := range cmd.Resources.GpuDeviceIds {
					ids[i] = strconv.Itoa(int(idx))
				}
				req["DeviceIDs"] = ids
			} else {
				req["Count"] = int(cmd.Resources.GpuCount)
			}
			host["DeviceRequests"] = []map[string]any{req}
		}
	}

	buf, _ := json.Marshal(body)
	resp, err := c.doJSON(ctx, "POST", "/containers/create", bytes.NewReader(buf), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Id string `json:"Id"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", err
	}
	if out.Id == "" {
		return "", fmt.Errorf("empty container id; response: %s", string(resp))
	}

	if _, err := c.do(ctx, "POST", "/containers/"+out.Id+"/start", nil); err != nil {
		return "", err
	}
	return out.Id, nil
}

func (c *Client) pull(ctx context.Context, image string) error {
	body, _ := json.Marshal(map[string]string{"image": image})
	_, err := c.doJSON(ctx, "POST", "/images/create?fromImage="+image, bytes.NewReader(body), nil)
	return err
}

func (c *Client) Stop(ctx context.Context, dockerID string, remove bool) error {
	if _, err := c.do(ctx, "POST", "/containers/"+dockerID+"/stop?t=10", nil); err != nil {
		// ignore "already stopped" / "no such container"
		if !strings.Contains(err.Error(), "is not running") &&
			!strings.Contains(err.Error(), "No such container") {
			return err
		}
	}
	if remove {
		if _, err := c.do(ctx, "DELETE", "/containers/"+dockerID+"?v=1&force=1", nil); err != nil {
			return err
		}
	}
	return nil
}

// Start starts an existing (created but stopped) container. Returns an
// error if the container is already running or no longer exists.
func (c *Client) Start(ctx context.Context, dockerID string) error {
	_, err := c.do(ctx, "POST", "/containers/"+dockerID+"/start", nil)
	return err
}

// Inspect returns the raw inspect JSON.
func (c *Client) Inspect(ctx context.Context, dockerID string) (map[string]any, error) {
	out, err := c.doJSON(ctx, "GET", "/containers/"+dockerID+"/json", nil, nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// MappedHostPort finds the host port that Docker mapped for the given container port.
func (c *Client) MappedHostPort(ctx context.Context, dockerID string, containerPort uint16) (uint16, error) {
	insp, err := c.Inspect(ctx, dockerID)
	if err != nil {
		return 0, err
	}
	ns, _ := insp["NetworkSettings"].(map[string]any)
	ports, _ := ns["Ports"].(map[string]any)
	for k, v := range ports {
		keyPort, proto, ok := splitPortKey(k)
		if !ok || uint16(keyPort) != containerPort {
			continue
		}
		_ = proto
		binds, _ := v.([]any)
		for _, b := range binds {
			m, _ := b.(map[string]any)
			hostPortStr, _ := m["HostPort"].(string)
			var p uint16
			if _, err := fmt.Sscanf(hostPortStr, "%d", &p); err == nil && p > 0 {
				return p, nil
			}
		}
	}
	return 0, fmt.Errorf("no host port mapping for %d in container %s", containerPort, shortID(dockerID))
}

func splitPortKey(k string) (int, string, bool) {
	idx := strings.Index(k, "/")
	if idx < 0 {
		return 0, "", false
	}
	var n int
	if _, err := fmt.Sscanf(k[:idx], "%d", &n); err != nil {
		return 0, "", false
	}
	return n, k[idx+1:], true
}

// ---- Events ----

// ContainerEvent is one lifecycle event from Docker's /events stream,
// filtered server-side to pumpkinpie.managed=true containers. The
// caller maps Action to the master's state vocabulary; the agent
// itself only cares about the raw action + the master container_id
// label so it can route the change to the right master-side row.
type ContainerEvent struct {
	ID         string            // docker container id
	Action     string            // start / die / stop / kill / destroy / pause / unpause / restart
	Time       time.Time         // when Docker recorded the event
	Attributes map[string]string // labels — pumpkinpie.container_id is the master-assigned id
}

// Events opens a long-poll HTTP stream to /events and forwards each
// container event onto the returned channel. The channel is closed
// when ctx is cancelled or the underlying connection drops.
//
// `since` lets the caller resume after a disconnect: Docker replays
// every event with time >= since, so the caller is responsible for
// passing the timestamp of the last successfully delivered event and
// deduping the overlap (the boundary event itself is replayed).
//
// Server-side filtering narrows the stream to containers labelled
// with pumpkinpie.managed=true, so we don't burn CPU on every event
// for unrelated containers running on the same host.
//
// NOTE: Docker's /events endpoint is unreliable on some platforms —
// Docker Desktop for Mac closes the long-poll connection immediately,
// and some embedded container runtimes don't implement it at all.
// Callers should treat a stream that closes faster than expected as
// a platform-level signal rather than a fatal error and run a polling
// fallback in parallel.
func (c *Client) Events(ctx context.Context, since time.Time) (<-chan ContainerEvent, error) {
	filter, _ := json.Marshal(map[string]any{
		"type":  "container",
		"label": map[string]string{"pumpkinpie.managed": "true"},
	})
	path := "/events?filters=" + queryEscape(string(filter))
	if !since.IsZero() {
		path += "&since=" + strconv.FormatInt(since.Unix(), 10)
	}
	url := "http://localhost" + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker events: %w", err)
	}
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("docker %s %s -> %d: %s", "GET", path, resp.StatusCode, string(buf))
	}

	out := make(chan ContainerEvent, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var raw struct {
				Status string `json:"status"`
				ID     string `json:"id"`
				Type   string `json:"Type"`
				Action string `json:"Action"`
				Time   int64  `json:"time"` // unix seconds
				Actor  struct {
					ID         string            `json:"ID"`
					Attributes map[string]string `json:"Attributes"`
				} `json:"Actor"`
			}
			if err := dec.Decode(&raw); err != nil {
				if err != io.EOF {
					log.Printf("docker events: decode: %v", err)
				}
				return
			}
			if raw.Type != "" && raw.Type != "container" {
				continue
			}
			ev := ContainerEvent{
				ID:         raw.Actor.ID,
				Action:     raw.Action,
				Attributes: raw.Actor.Attributes,
			}
			if raw.ID != "" && ev.ID == "" {
				ev.ID = raw.ID
			}
			if raw.Time > 0 {
				ev.Time = time.Unix(raw.Time, 0)
			}
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

// ListAgentContainers returns containers labeled with our agent:master_container_id
func (c *Client) ListAgent(ctx context.Context) ([]*pb.ContainerInfo, error) {
	filter, _ := json.Marshal(map[string]any{
		"label": []string{"pumpkinpie.managed=true"},
	})
	out, err := c.doJSON(ctx, "GET", "/containers/json?all=1&filters="+queryEscape(string(filter)), nil, nil)
	if err != nil {
		return nil, err
	}
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil {
		return nil, err
	}
	res := make([]*pb.ContainerInfo, 0, len(arr))
	for _, ci := range arr {
		id, _ := ci["Labels"].(map[string]any)
		cidStr, _ := id["pumpkinpie.container_id"].(string)
		if cidStr == "" {
			continue
		}
		ports := []*pb.PortMapping{}
		if ps, ok := ci["Ports"].([]any); ok {
			for _, p := range ps {
				pm, _ := p.(map[string]any)
				pp, _ := pm["PrivatePort"].(float64)
				tp, _ := pm["Type"].(string)
				ports = append(ports, &pb.PortMapping{
					ContainerPort: uint32(pp),
					Protocol:      tp,
				})
			}
		}
		names, _ := ci["Names"].([]any)
		name := ""
		if len(names) > 0 {
			name, _ = names[0].(string)
			name = strings.TrimPrefix(name, "/")
		}
		state, _ := ci["State"].(string)
		status, _ := ci["Status"].(string)
		image, _ := ci["Image"].(string)
		dockerID, _ := ci["Id"].(string)
		res = append(res, &pb.ContainerInfo{
			ContainerId: cidStr,
			DockerId:    dockerID,
			Name:        name,
			Image:       image,
			State:       state,
			Status:      status,
			Ports:       ports,
		})
	}
	return res, nil
}

// ---- low-level ----

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	return c.doJSON(ctx, method, path, body, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader, _ map[string]string) ([]byte, error) {
	url := "http://localhost" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("docker %s %s -> %d: %s", method, path, resp.StatusCode, string(buf))
	}
	return buf, nil
}

func queryEscape(s string) string {
	// minimal escape
	r := strings.NewReplacer(" ", "%20", "\"", "%22", "{", "%7B", "}", "%7D", "[", "%5B", "]", "%5D", ",", "%2C")
	return r.Replace(s)
}

// urlPathEscape escapes a string for use inside a URL path segment.
// Unlike queryEscape, it preserves '/' so registry paths like
// "myregistry.io/app:1.0" round-trip correctly.
func urlPathEscape(s string) string {
	r := strings.NewReplacer(
		" ", "%20",
		"\"", "%22",
		"#", "%23",
		"?", "%3F",
		"@", "%40",
		":", "%3A",
	)
	return r.Replace(s)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
