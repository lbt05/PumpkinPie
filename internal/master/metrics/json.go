package metrics

import "encoding/json"

// jsonDisk mirrors the on-the-wire shape produced by
// jsonMarshalDisks in internal/master/agentmgr/manager.go and is
// kept here so the metrics package can decode the persisted
// nodes.disk_json column without importing agentmgr.
type jsonDisk struct {
	Path         string  `json:"path"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsedBytes    uint64  `json:"used_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

// DecodeDisks parses the JSON-blob form of node disks. Returns nil on
// any error — the disk list is a "nice to have" view, never the
// authoritative state, so missing data should not poison the metric.
func DecodeDisks(s string) []DiskSample {
	if s == "" {
		return nil
	}
	var raw []jsonDisk
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make([]DiskSample, len(raw))
	for i, d := range raw {
		out[i] = DiskSample{
			Path:         d.Path,
			TotalBytes:   d.TotalBytes,
			UsedBytes:    d.UsedBytes,
			UsagePercent: d.UsagePercent,
		}
	}
	return out
}