package scheduler

import (
	"context"
	"errors"
	"math"

	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
)

// ResourceRequest is what the user wants for a new container.
type ResourceRequest struct {
	CPUCores   float64 // 0 = unlimited
	MemoryBytes uint64 // 0 = unlimited
	GPUCount   uint32  // 0 = no GPU
}

// ErrNoNode means no online node satisfies the request.
var ErrNoNode = errors.New("no suitable node available")

// Select returns the best online node that satisfies req and has the lowest
// combined utilization score.
func Select(ctx context.Context, st *store.Store, req ResourceRequest) (*store.Node, error) {
	nodes, err := st.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var best *store.Node
	bestScore := math.Inf(1)
	for _, n := range nodes {
		if n.State != "online" {
			continue
		}
		if !nodeSatisfies(n, req) {
			continue
		}
		s := score(n)
		if s < bestScore {
			bestScore = s
			best = n
		}
	}
	if best == nil {
		return nil, ErrNoNode
	}
	return best, nil
}

func nodeSatisfies(n *store.Node, req ResourceRequest) bool {
	if req.GPUCount > 0 && n.GpuCount < req.GPUCount {
		return false
	}
	if req.CPUCores > 0 && float64(n.CPUCores) > 0 && float64(n.CPUCores) < req.CPUCores {
		return false
	}
	if req.MemoryBytes > 0 && n.MemTotalBytes > 0 && n.MemTotalBytes < req.MemoryBytes {
		return false
	}
	return true
}

// Score: lower is better. Weighted sum of CPU%, MEM%, GPU usage%, gpu mem%.
func score(n *store.Node) float64 {
	cpu := n.CPUPercent / 100.0
	mem := 0.0
	if n.MemTotalBytes > 0 {
		mem = float64(n.MemUsedBytes) / float64(n.MemTotalBytes)
	}
	gpu := n.GpuUsageAvg / 100.0
	gpuMem := 0.0
	if n.GpuMemTotal > 0 {
		gpuMem = float64(n.GpuMemUsed) / float64(n.GpuMemTotal)
	}
	return 0.4*cpu + 0.3*mem + 0.2*gpu + 0.1*gpuMem
}
