package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"

	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

func newContainerID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "c-" + hex.EncodeToString(b[:])
}

func externalURL(port uint32) string {
	return "http://localhost:" + strconv.FormatUint(uint64(port), 10) + "/"
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
