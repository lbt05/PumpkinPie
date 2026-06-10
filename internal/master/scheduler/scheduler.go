package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
)

// ResourceRequest is what the user wants for a new container.
type ResourceRequest struct {
	CPUCores    float64 // 0 = unlimited
	MemoryBytes uint64  // 0 = unlimited
	GPUCount    uint32  // 0 = no GPU
}

// ErrNoNode means no online node satisfies the request.
var ErrNoNode = errors.New("no suitable node available")

// Select returns the best online node that satisfies req and has the lowest
// combined utilization score. GPU availability is computed against the
// store's live allocation table so contended GPUs are not double-counted.
func Select(ctx context.Context, st *store.Store, req ResourceRequest) (*store.Node, error) {
	nodes, err := st.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	gpuUsed, err := st.GPUUsageByNode(ctx)
	if err != nil {
		return nil, err
	}

	var best *store.Node
	bestScore := math.Inf(1)
	stats := selectStats{}
	for _, n := range nodes {
		if n.State != "online" {
			continue
		}
		stats.online++

		freeGPU := freeGPUsOn(n, gpuUsed)
		if req.GPUCount > 0 && freeGPU < req.GPUCount {
			continue
		}
		stats.gpuOK++

		if req.CPUCores > 0 && float64(n.CPUCores) > 0 && float64(n.CPUCores) < req.CPUCores {
			continue
		}
		stats.cpuOK++

		if req.MemoryBytes > 0 && n.MemTotalBytes > 0 && n.MemTotalBytes < req.MemoryBytes {
			continue
		}
		stats.memOK++

		s := score(n)
		if s < bestScore {
			bestScore = s
			best = n
		}
	}
	if best == nil {
		return nil, rejectionError(req, stats)
	}
	return best, nil
}

// FreeGPUs returns the number of unallocated GPUs on the node, taking into
// account the live allocation table. Exposed for callers that need to
// double-check a candidate node before a separate Alloc call.
func FreeGPUs(n *store.Node, gpuUsed map[string]uint32) uint32 {
	return freeGPUsOn(n, gpuUsed)
}

func freeGPUsOn(n *store.Node, gpuUsed map[string]uint32) uint32 {
	used := gpuUsed[n.ID]
	if used >= n.GpuCount {
		return 0
	}
	return n.GpuCount - used
}

type selectStats struct {
	online int
	gpuOK  int
	cpuOK  int
	memOK  int
}

func rejectionError(req ResourceRequest, s selectStats) error {
	parts := []string{fmt.Sprintf("%d online", s.online)}
	if req.GPUCount > 0 {
		parts = append(parts, fmt.Sprintf("%d with >=%d free GPUs", s.gpuOK, req.GPUCount))
	}
	if req.CPUCores > 0 {
		parts = append(parts, fmt.Sprintf("%d with >=%.2f CPU cores", s.cpuOK, req.CPUCores))
	}
	if req.MemoryBytes > 0 {
		parts = append(parts, fmt.Sprintf("%d with >=%d bytes memory", s.memOK, req.MemoryBytes))
	}
	return fmt.Errorf("%w: %s", ErrNoNode, joinComma(parts))
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
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
