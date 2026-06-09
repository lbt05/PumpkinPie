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
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Client struct {
	host   string // e.g. unix:///var/run/docker.sock
	http   *http.Client
	apiVer string
}

func New() (*Client, error) {
	sockPath := dockerSocketPath()
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

func dockerSocketPath() string {
	if v := os.Getenv("DOCKER_SOCK"); v != "" {
		return v
	}
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		// support unix:///path
		if strings.HasPrefix(v, "unix://") {
			return strings.TrimPrefix(v, "unix://")
		}
	}
	return "/var/run/docker.sock"
}

func (c *Client) Close() error { return nil }

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "/_ping", nil)
	return err
}

// Create starts a container. Returns the docker container ID.
func (c *Client) Create(ctx context.Context, cmd *pb.CreateContainerCommand) (string, error) {
	if cmd.Pull {
		if err := c.pull(ctx, cmd.Image); err != nil {
			return "", fmt.Errorf("pull %s: %w", cmd.Image, err)
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
		portBindings[key] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "0"}}
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
			host["DeviceRequests"] = []map[string]any{{
				"Count":        int(cmd.Resources.GpuCount),
				"Capabilities": [][]string{{"gpu"}},
			}}
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

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
