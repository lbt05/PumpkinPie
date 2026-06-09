package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

type Node struct {
	ID            string    `json:"id"`
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
	ID            string    `json:"id"`
	NodeID        string    `json:"node_id"`
	DockerID      string    `json:"docker_id"`
	Name          string    `json:"name"`
	Image         string    `json:"image"`
	State         string    `json:"state"`
	Status        string    `json:"status"`
	EnvJSON       string    `json:"-"`
	CmdJSON       string    `json:"-"`
	PortsJSON     string    `json:"-"`
	VolumeBinds   string    `json:"-"`
	CPUCores      float64   `json:"cpu_cores"`
	MemoryBytes   uint64    `json:"memory_bytes"`
	GPUCount      uint32    `json:"gpu_count"`
	ExternalPort  uint32    `json:"external_port"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
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

CREATE TABLE IF NOT EXISTS containers (
	id              TEXT PRIMARY KEY,
	node_id         TEXT NOT NULL,
	docker_id       TEXT,
	name            TEXT,
	image           TEXT NOT NULL,
	state           TEXT,
	status          TEXT,
	env_json        TEXT,
	cmd_json        TEXT,
	ports_json      TEXT,
	volume_binds    TEXT,
	cpu_cores       REAL DEFAULT 0,
	memory_bytes    INTEGER DEFAULT 0,
	gpu_count       INTEGER DEFAULT 0,
	external_port   INTEGER,
	created_at      DATETIME NOT NULL,
	updated_at      DATETIME NOT NULL,
	FOREIGN KEY(node_id) REFERENCES nodes(id)
);
CREATE INDEX IF NOT EXISTS idx_containers_node ON containers(node_id);

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
INSERT INTO nodes (id, name, hostname, os, arch, agent_version, state, last_heartbeat, registered_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, hostname=excluded.hostname, os=excluded.os, arch=excluded.arch,
  agent_version=excluded.agent_version, state=excluded.state, last_heartbeat=excluded.last_heartbeat
`, n.ID, n.Name, n.Hostname, n.OS, n.Arch, n.AgentVersion, n.State,
		n.LastHeartbeat, n.RegisteredAt)
	return err
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, hostname, os, arch, agent_version, state,
		last_heartbeat, registered_at, cpu_percent, cpu_cores, mem_used_bytes, mem_total_bytes,
		COALESCE(disk_json, ''), COALESCE(gpu_json, ''), load1, gpu_count, gpu_mem_used, gpu_mem_total, gpu_usage_avg, metrics_at
		FROM nodes ORDER BY registered_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n := &Node{}
		var lastHB, metricsAt sql.NullTime
		if err := rows.Scan(&n.ID, &n.Name, &n.Hostname, &n.OS, &n.Arch, &n.AgentVersion, &n.State,
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
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, hostname, os, arch, agent_version, state,
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
	n := &Node{}
	var lastHB, metricsAt sql.NullTime
	err = rows.Scan(&n.ID, &n.Name, &n.Hostname, &n.OS, &n.Arch, &n.AgentVersion, &n.State,
		&lastHB, &n.RegisteredAt, &n.CPUPercent, &n.CPUCores, &n.MemUsedBytes, &n.MemTotalBytes,
		&n.DiskJSON, &n.GPUJSON, &n.Load1, &n.GpuCount, &n.GpuMemUsed, &n.GpuMemTotal, &n.GpuUsageAvg, &metricsAt)
	if err == nil {
		if lastHB.Valid {
			n.LastHeartbeat = lastHB.Time
		}
		if metricsAt.Valid {
			n.MetricsAt = metricsAt.Time
		}
	}
	return n, err
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
	ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count, external_port, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.NodeID, c.DockerID, c.Name, c.Image, c.State, c.Status, c.EnvJSON, c.CmdJSON,
		c.PortsJSON, c.VolumeBinds, c.CPUCores, c.MemoryBytes, c.GPUCount, c.ExternalPort, c.CreatedAt, c.UpdatedAt)
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

func (s *Store) ListContainers(ctx context.Context) ([]*Container, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, docker_id, name, image, state, status,
		env_json, cmd_json, ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count,
		external_port, created_at, updated_at FROM containers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Container
	for rows.Next() {
		c := &Container{}
		if err := rows.Scan(&c.ID, &c.NodeID, &c.DockerID, &c.Name, &c.Image, &c.State, &c.Status,
			&c.EnvJSON, &c.CmdJSON, &c.PortsJSON, &c.VolumeBinds, &c.CPUCores, &c.MemoryBytes, &c.GPUCount,
			&c.ExternalPort, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetContainer(ctx context.Context, id string) (*Container, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, docker_id, name, image, state, status,
		env_json, cmd_json, ports_json, volume_binds, cpu_cores, memory_bytes, gpu_count,
		external_port, created_at, updated_at FROM containers WHERE id=?`, id)
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
		&c.ExternalPort, &c.CreatedAt, &c.UpdatedAt)
	return c, err
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
