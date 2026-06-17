package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Sentinel errors for GPU allocation.
var (
	ErrInsufficientGPUs = errors.New("insufficient free GPUs on node")
	ErrGPUTaken         = errors.New("requested GPU index is already allocated")
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

type Node struct {
	ID            string    `json:"id"`
	MachineID     string    `json:"machine_id"`
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	AgentVersion  string    `json:"agent_version"`
	State         string    `json:"state"` // online / offline
	LastHeartbeat time.Time `json:"last_heartbeat"`
	RegisteredAt  time.Time `json:"registered_at"`
	// last snapshot
	CPUPercent     float64   `json:"cpu_percent"`
	CPUCores       uint32    `json:"cpu_cores"`
	MemUsedBytes   uint64    `json:"mem_used_bytes"`
	MemTotalBytes  uint64    `json:"mem_total_bytes"`
	DiskJSON       string    `json:"-"`
	GPUJSON        string    `json:"-"`
	Load1          float64   `json:"load1"`
	GpuCount       uint32    `json:"gpu_count"`
	GpuMemUsed     uint64    `json:"gpu_mem_used_bytes"`
	GpuMemTotal    uint64    `json:"gpu_mem_total_bytes"`
	GpuUsageAvg    float64   `json:"gpu_usage_percent"`
	MetricsAt      time.Time `json:"metrics_at"`
}

type Container struct {
	ID             string    `json:"id"`
	NodeID         string    `json:"node_id"`
	DockerID       string    `json:"docker_id"`
	Name           string    `json:"name"`
	Image          string    `json:"image"`
	State          string    `json:"state"`
	Status         string    `json:"status"`
	EnvJSON        string    `json:"-"`
	CmdJSON        string    `json:"-"`
	PortsJSON      string    `json:"-"`
	VolumeBinds    string    `json:"-"`
	CPUCores       float64   `json:"cpu_cores"`
	MemoryBytes    uint64    `json:"memory_bytes"`
	GPUCount       uint32    `json:"gpu_count"`
	GPUIndicesJSON string    `json:"-"`
	ExternalPort   uint32    `json:"external_port"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id              TEXT PRIMARY KEY,
	machine_id      TEXT NOT NULL DEFAULT '',
	name            TEXT NOT NULL,
	hostname        TEXT,
	os              TEXT,
	arch            TEXT,
	agent_version   TEXT,
	state           TEXT NOT NULL DEFAULT 'offline',
	last_heartbeat  DATETIME,
	registered_at   DATETIME NOT NULL,
	cpu_percent     REAL DEFAULT 0,
	cpu_cores       INTEGER DEFAULT 0,
	mem_used_bytes  INTEGER DEFAULT 0,
	mem_total_bytes INTEGER DEFAULT 0,
	disk_json       TEXT,
	gpu_json        TEXT,
	load1           REAL DEFAULT 0,
	gpu_count       INTEGER DEFAULT 0,
	gpu_mem_used    INTEGER DEFAULT 0,
	gpu_mem_total   INTEGER DEFAULT 0,
	gpu_usage_avg   REAL DEFAULT 0,
	metrics_at      DATETIME
);
CREATE INDEX IF NOT EXISTS idx_nodes_state ON nodes(state);
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_machine_id ON nodes(machine_id) WHERE machine_id <> '';

CREATE TABLE IF NOT EXISTS containers (
	id                TEXT PRIMARY KEY,
	node_id           TEXT NOT NULL,
	docker_id         TEXT,
	name              TEXT,
	image             TEXT NOT NULL,
	state             TEXT,
	status            TEXT,
	env_json          TEXT,
	cmd_json          TEXT,
	ports_json        TEXT,
	volume_binds      TEXT,
	cpu_cores         REAL DEFAULT 0,
	memory_bytes      INTEGER DEFAULT 0,
	gpu_count         INTEGER DEFAULT 0,
	gpu_indices_json  TEXT NOT NULL DEFAULT '',
	external_port     INTEGER,
	created_at        DATETIME NOT NULL,
	updated_at        DATETIME NOT NULL,
	FOREIGN KEY(node_id) REFERENCES nodes(id)
);
CREATE INDEX IF NOT EXISTS idx_containers_node ON containers(node_id);

CREATE TABLE IF NOT EXISTS gpu_alloc (
	node_id       TEXT NOT NULL,
	gpu_index     INTEGER NOT NULL,
	container_id  TEXT NOT NULL,
	allocated_at  DATETIME NOT NULL,
	PRIMARY KEY (node_id, gpu_index),
	FOREIGN KEY (container_id) REFERENCES containers(id) ON DELETE CASCADE,
	FOREIGN KEY (node_id)      REFERENCES nodes(id)
);
CREATE INDEX IF NOT EXISTS idx_gpu_alloc_container ON gpu_alloc(container_id);

CREATE TABLE IF NOT EXISTS port_alloc (
	port            INTEGER PRIMARY KEY,
	container_id    TEXT NOT NULL
);
`

// ---------- Nodes ----------

func (s *Store) UpsertNode(ctx context.Context, n *Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.RegisteredAt.IsZero() {
		n.RegisteredAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO nodes (id, machine_id, name, hostname, os, arch, agent_version, state, last_heartbeat, registered_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  machine_id=excluded.machine_id, name=excluded.name, hostname=excluded.hostname,
  os=excluded.os, arch=excluded.arch, agent_version=excluded.agent_version,
  state=excluded.state, last_heartbeat=excluded.last_heartbeat
`, n.ID, n.MachineID, n.Name, n.Hostname, n.OS, n.Arch, n.AgentVersion, n.State,
		n.LastHeartbeat, n.RegisteredAt)
	return err
}

// GetNodeByMachineID returns the node with the given machine_id, or
// sql.ErrNoRows if none matches. machineID == "" never matches.
func (s *Store) GetNodeByMachineID(ctx context.Context, machineID string) (*Node, error) {
	if machineID == "" {
		return nil, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, machine_id, name, hostname, os, arch, agent_version, state,
		last_heartbeat, registered_at, cpu_percent, cpu_cores, mem_used_bytes, mem_total_bytes,
		COALESCE(disk_json, ''), COALESCE(gpu_json, ''), load1, gpu_count, gpu_mem_used, gpu_mem_total, gpu_usage_avg, metrics_at
		FROM nodes WHERE machine_id=?`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	return scanNode(rows)
}

func (s *Store) UpdateNodeState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET state=?, last_heartbeat=? WHERE id=?`,
		state, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateNodeMetrics(ctx context.Context, n *Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
UPDATE nodes SET cpu_percent=?, cpu_cores=?, mem_used_bytes=?, mem_total_bytes=?,
	disk_json=?, gpu_json=?, load1=?, gpu_count=?, gpu_mem_used=?, gpu_mem_total=?,
	gpu_usage_avg=?, metrics_at=?, last_heartbeat=?
WHERE id=?`,
		n.CPUPercent, n.CPUCores, n.MemUsedBytes, n.MemTotalBytes,
		n.DiskJSON, n.GPUJSON, n.Load1, n.GpuCount, n.GpuMemUsed, n.GpuMemTotal,
		n.GpuUsageAvg, time.Now().UTC(), time.Now().UTC(), n.ID)
	return err
}

func (s *Store) ListNodes(ctx context.Context) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, machine_id, name, hostname, os, arch, agent_version, state,
		last_heartbeat, registered_at, cpu_percent, cpu_cores, mem_used_bytes, mem_total_bytes,
		COALESCE(disk_json, ''), COALESCE(gpu_json, ''), load1, gpu_count, gpu_mem_used, gpu_mem_total, gpu_usage_avg, metrics_at
		FROM nodes ORDER BY registered_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, machine_id, name, hostname, os, arch, agent_version, state,
		last_heartbeat, registered_at, cpu_percent, cpu_cores, mem_used_bytes, mem_total_bytes,
		COALESCE(disk_json, ''), COALESCE(gpu_json, ''), load1, gpu_count, gpu_mem_used, gpu_mem_total, gpu_usage_avg, metrics_at
		FROM nodes WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	return scanNode(rows)
}

func scanNode(rows *sql.Rows) (*Node, error) {
	n := &Node{}
	var lastHB, metricsAt sql.NullTime
	if err := rows.Scan(&n.ID, &n.MachineID, &n.Name, &n.Hostname, &n.OS, &n.Arch, &n.AgentVersion, &n.State,
		&lastHB, &n.RegisteredAt, &n.CPUPercent, &n.CPUCores, &n.MemUsedBytes, &n.MemTotalBytes,
		&n.DiskJSON, &n.GPUJSON, &n.Load1, &n.GpuCount, &n.GpuMemUsed, &n.GpuMemTotal, &n.GpuUsageAvg, &metricsAt); err != nil {
		return nil, err
	}
	if lastHB.Valid {
		n.LastHeartbeat = lastHB.Time
	}
	if metricsAt.Valid {
		n.MetricsAt = metricsAt.Time
	}
	return n, nil
}

// ---------- Containers ----------

func (s *Store) InsertContainer(ctx context.Context, c *Container) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO containers (id, node_id, docker_id, name, image, state, status, env_json, cmd_json,
	ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count, gpu_indices_json,
	external_port, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.NodeID, c.DockerID, c.Name, c.Image, c.State, c.Status, c.EnvJSON, c.CmdJSON,
		c.PortsJSON, c.VolumeBinds, c.CPUCores, c.MemoryBytes, c.GPUCount, c.GPUIndicesJSON,
		c.ExternalPort, c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *Store) UpdateContainerState(ctx context.Context, id, dockerID, state, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE containers SET docker_id=COALESCE(NULLIF(?,''), docker_id),
		state=?, status=?, updated_at=? WHERE id=?`,
		dockerID, state, status, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateContainerExternalPort(ctx context.Context, id string, port uint32) error {
	_, err := s.db.ExecContext(ctx, `UPDATE containers SET external_port=?, updated_at=? WHERE id=?`,
		port, time.Now().UTC(), id)
	return err
}

func (s *Store) DeleteContainer(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM containers WHERE id=?`, id)
	return err
}

// ContainerExists reports whether a row exists for the given master
// container id. Cheaper than GetContainer for the common "is this a
// known container?" check (e.g. before processing an unsolicited
// lifecycle event from the agent).
func (s *Store) ContainerExists(ctx context.Context, id string) (bool, error) {
	var x int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM containers WHERE id=? LIMIT 1`, id).Scan(&x)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ListContainers(ctx context.Context) ([]*Container, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, docker_id, name, image, state, status,
		env_json, cmd_json, ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count,
		COALESCE(gpu_indices_json, ''), external_port, created_at, updated_at
		FROM containers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Container
	for rows.Next() {
		c := &Container{}
		if err := rows.Scan(&c.ID, &c.NodeID, &c.DockerID, &c.Name, &c.Image, &c.State, &c.Status,
			&c.EnvJSON, &c.CmdJSON, &c.PortsJSON, &c.VolumeBinds, &c.CPUCores, &c.MemoryBytes, &c.GPUCount,
			&c.GPUIndicesJSON, &c.ExternalPort, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetContainer(ctx context.Context, id string) (*Container, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, docker_id, name, image, state, status,
		env_json, cmd_json, ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count,
		COALESCE(gpu_indices_json, ''), external_port, created_at, updated_at
		FROM containers WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	c := &Container{}
	err = rows.Scan(&c.ID, &c.NodeID, &c.DockerID, &c.Name, &c.Image, &c.State, &c.Status,
		&c.EnvJSON, &c.CmdJSON, &c.PortsJSON, &c.VolumeBinds, &c.CPUCores, &c.MemoryBytes, &c.GPUCount,
		&c.GPUIndicesJSON, &c.ExternalPort, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// UpdateContainerGPUIndices replaces the persisted indices for a container.
func (s *Store) UpdateContainerGPUIndices(ctx context.Context, id, indicesJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE containers SET gpu_indices_json=?, updated_at=? WHERE id=?`,
		indicesJSON, time.Now().UTC(), id)
	return err
}

// ---------- Port allocation ----------

func (s *Store) AllocPort(ctx context.Context, start, end uint32, containerID string) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// find smallest free port in [start, end]
	rows, err := tx.QueryContext(ctx, `SELECT port FROM port_alloc WHERE port>=? AND port<=? ORDER BY port`, start, end)
	if err != nil {
		return 0, err
	}
	used := map[uint32]bool{}
	for rows.Next() {
		var p uint32
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		used[p] = true
	}
	rows.Close()
	for p := start; p <= end; p++ {
		if !used[p] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO port_alloc(port, container_id) VALUES(?, ?)`, p, containerID); err != nil {
				return 0, err
			}
			if err := tx.Commit(); err != nil {
				return 0, err
			}
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in [%d,%d]", start, end)
}

func (s *Store) FreePort(ctx context.Context, port uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM port_alloc WHERE port=?`, port)
	return err
}

// ---------- GPU allocation ----------

// AllocGPUs picks `count` lowest-numbered free GPU indices on nodeID and
// reserves them for containerID. Returns the chosen indices in ascending
// order. If fewer than `count` are free, returns ErrInsufficientGPUs and
// reserves none. Atomic via PK(node_id, gpu_index): concurrent callers
// racing for the same indices get a UNIQUE-constraint failure and roll back.
func (s *Store) AllocGPUs(ctx context.Context, nodeID, containerID string, count int, total uint32) ([]uint32, error) {
	if count <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	used, err := readUsedIndices(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	chosen := make([]uint32, 0, count)
	for i := uint32(0); i < total && len(chosen) < count; i++ {
		if !used[i] {
			chosen = append(chosen, i)
		}
	}
	if len(chosen) < count {
		return nil, fmt.Errorf("%w: requested %d, %d free of %d", ErrInsufficientGPUs, count, len(chosen), total)
	}
	now := time.Now().UTC()
	for _, idx := range chosen {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gpu_alloc(node_id, gpu_index, container_id, allocated_at) VALUES(?, ?, ?, ?)`,
			nodeID, idx, containerID, now); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: GPU %d on node %s", ErrGPUTaken, idx, nodeID)
			}
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return chosen, nil
}

// AllocSpecificGPUs reserves exactly the given indices for containerID on
// nodeID. If any one of them is already allocated, none are reserved and
// the call returns ErrGPUTaken.
func (s *Store) AllocSpecificGPUs(ctx context.Context, nodeID, containerID string, indices []uint32) error {
	if len(indices) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, idx := range indices {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gpu_alloc(node_id, gpu_index, container_id, allocated_at) VALUES(?, ?, ?, ?)`,
			nodeID, idx, containerID, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: GPU %d on node %s", ErrGPUTaken, idx, nodeID)
			}
			return err
		}
	}
	return tx.Commit()
}

// FreeGPUs releases every GPU reservation held by containerID. Safe to
// call on a container with no allocations.
func (s *Store) FreeGPUs(ctx context.Context, containerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM gpu_alloc WHERE container_id=?`, containerID)
	return err
}

// GetContainerGPUs returns the indices currently reserved by containerID,
// in ascending order. Empty slice if the container holds no GPUs.
func (s *Store) GetContainerGPUs(ctx context.Context, containerID string) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT gpu_index FROM gpu_alloc WHERE container_id=? ORDER BY gpu_index`, containerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var i uint32
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// GPUUsageByNode returns the count of reserved GPUs per node.
func (s *Store) GPUUsageByNode(ctx context.Context) (map[string]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, COUNT(*) FROM gpu_alloc GROUP BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint32{}
	for rows.Next() {
		var node string
		var n uint32
		if err := rows.Scan(&node, &n); err != nil {
			return nil, err
		}
		out[node] = n
	}
	return out, rows.Err()
}

// GPUAlloc is a single reservation row enriched with the holding
// container's human name. Returned by ListGPUAllocsForNode.
type GPUAlloc struct {
	Index         uint32
	ContainerID   string
	ContainerName string
}

// ListGPUAllocsForNode returns every GPU reservation on nodeID, with
// the holding container's name joined in. Indices appear in ascending
// order. Empty slice if the node has no allocations.
func (s *Store) ListGPUAllocsForNode(ctx context.Context, nodeID string) ([]GPUAlloc, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT g.gpu_index, g.container_id, COALESCE(c.name, '')
  FROM gpu_alloc g
  LEFT JOIN containers c ON c.id = g.container_id
 WHERE g.node_id = ?
 ORDER BY g.gpu_index`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GPUAlloc
	for rows.Next() {
		var a GPUAlloc
		if err := rows.Scan(&a.Index, &a.ContainerID, &a.ContainerName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func readUsedIndices(ctx context.Context, tx *sql.Tx, nodeID string) (map[uint32]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT gpu_index FROM gpu_alloc WHERE node_id=?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[uint32]bool{}
	for rows.Next() {
		var i uint32
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		used[i] = true
	}
	return used, rows.Err()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite surfaces these as "constraint failed: UNIQUE constraint failed: ..."
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}
