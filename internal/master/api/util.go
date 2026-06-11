package api

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

func newContainerID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "c-" + hex.EncodeToString(b[:])
}

func externalURL(c *gin.Context, port uint32) string {
	host := "localhost"
	if c != nil && c.Request != nil && c.Request.Host != "" {
		h := c.Request.Host
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if h != "" {
			host = h
		}
	}
	return "http://" + host + ":" + strconv.FormatUint(uint64(port), 10) + "/"
}

// autoName generates a human-readable name from an image reference when
// the user left the field blank. Examples:
//
//	"nginx:alpine"        -> "pp-nginx-alpine-x7t2c"
//	"myregistry.io/app:1" -> "pp-myregistry-io-app-1-x7t2c"
//	"redis"               -> "pp-redis-x7t2c"
//
// Algorithm: drop the registry host (everything up to and including the
// last '/'), then drop the tag separator (':' or '@') and everything
// after, then lower-case. Anything that is still illegal as a docker
// container name (e.g. '.', '/') gets folded in by sanitizeContainerName.
func autoName(image string) string {
	name := image
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexAny(name, ":@"); i >= 0 {
		// turn "nginx:1.0" into "nginx-1.0" so we keep the version
		name = name[:i] + "-" + name[i+1:]
	}
	if name == "" {
		name = "container"
	}
	return sanitizeContainerName("pp-" + name) + "-" + rand6()
}

func rand6() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	// 6 hex chars from 3 bytes
	return hex.EncodeToString(b[:])
}

// sanitizeContainerName makes a user-supplied name safe to pass to
// docker as a container name: lowercase, replace illegal chars with
// '-', collapse runs of '-', and trim to 64 chars.
func sanitizeContainerName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "pp-" + rand6()
	}
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "pp-" + rand6()
	}
	if len(out) > 64 {
		out = out[:57] + "-" + rand6()
	}
	if !(out[0] >= 'a' && out[0] <= 'z') && !(out[0] >= '0' && out[0] <= '9') {
		out = "x" + out
	}
	return out
}

type portMappingJSON struct {
	ContainerPort uint32 `json:"container_port"`
	Protocol      string `json:"protocol"`
	// HostPort is the port Docker binds on the agent host (the "Y" in
	// `docker run -p X:Y` where X is the agent listener and Y is the
	// container port). 0 means "use ContainerPort" — matches Docker's
	// own `-p 8888:8888` shorthand.
	HostPort uint32 `json:"host_port"`
}

// portMappingsToProto converts user-supplied port mappings into the
// proto representation sent to the agent. It fills in any unspecified
// HostPort (0) with the container port — the shorthand `docker run -p
// 8888:8888` shape. Protocol defaults to "tcp" when empty.
func portMappingsToProto(in []portMappingJSON) []*pb.PortMapping {
	out := make([]*pb.PortMapping, 0, len(in))
	for _, p := range in {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		hostPort := p.HostPort
		if hostPort == 0 {
			hostPort = p.ContainerPort
		}
		out = append(out, &pb.PortMapping{
			ContainerPort: p.ContainerPort,
			Protocol:      proto,
			HostPort:      hostPort,
		})
	}
	return out
}

func freeGPUsForRow(total, used uint32) uint32 {
	if used >= total {
		return 0
	}
	return total - used
}
