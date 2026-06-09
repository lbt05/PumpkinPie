package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

func newContainerID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "c-" + hex.EncodeToString(b[:])
}

func externalURL(proxyPort uint32, containerID string) string {
	return fmt.Sprintf("http://localhost:%d/c/%s/", proxyPort, containerID)
}

type portMappingJSON struct {
	ContainerPort uint32 `json:"container_port"`
	Protocol      string `json:"protocol"`
}

func parsePorts(s string) ([]portMappingJSON, error) {
	var out []portMappingJSON
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func asProto(in []portMappingJSON) []*pb.PortMapping {
	out := make([]*pb.PortMapping, 0, len(in))
	for _, p := range in {
		out = append(out, &pb.PortMapping{
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		})
	}
	return out
}
