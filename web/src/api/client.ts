import axios from 'axios'

export const api = axios.create({ baseURL: '/api' })

export type DiskInfo = {
  path: string
  total_bytes: number
  used_bytes: number
  usage_percent: number
}
export type GpuDevice = {
  index: number
  name: string
  uuid: string
  usage_percent: number
  mem_used_bytes: number
  mem_total_bytes: number
}
export type Node = {
  id: string
  name: string
  hostname: string
  os: string
  arch: string
  agent_version: string
  state: 'online' | 'offline'
  last_heartbeat: string
  registered_at: string
  cpu_percent: number
  cpu_cores: number
  /** Optional CPU model string (e.g. "AMD EPYC 7763 64-Core"). */
  cpu_model?: string
  mem_used_bytes: number
  mem_total_bytes: number
  load1: number
  load5?: number
  load15?: number
  gpu_count: number
  gpu_used?: number
  gpu_free?: number
  gpu_mem_used_bytes: number
  gpu_mem_total_bytes: number
  gpu_usage_percent: number
  metrics_at: string
  /** Optional: management IP shown in the node card spec strip. */
  ip?: string
  /** Optional: human-friendly role label (training / inference / general). */
  role?: string
  /** Optional: container runtime string (e.g. "docker 24.0.7"). */
  runtime?: string
  /** Optional: aggregate network bytes received since boot. */
  net_rx_bytes?: number
  /** Optional: aggregate network bytes sent since boot. */
  net_tx_bytes?: number
  disks?: DiskInfo[]
  gpus?: GpuDevice[]
}

export type PortMapping = { container_port: number; protocol: string }

export type Container = {
  id: string
  node_id: string
  node_name?: string
  docker_id?: string
  name: string
  image: string
  state: string
  status: string
  cpu_cores: number
  memory_bytes: number
  gpu_count: number
  gpu_indices?: number[]
  external_port: number
  external_url?: string
  env?: string[]
  cmd?: string[]
  ports?: PortMapping[]
  created_at: string
  updated_at: string
}

export async function listNodes() {
  const r = await api.get<{ nodes: Node[] }>('/nodes')
  return r.data.nodes
}

export type NodeGPUDevice = {
  index: number
  name?: string
  uuid?: string
  mem_total_bytes?: number
  held_by: { container_id: string; container_name?: string } | null
}

export async function listNodeGPUs(id: string) {
  const r = await api.get<{ gpus: NodeGPUDevice[] }>(`/nodes/${id}/gpus`)
  return r.data.gpus
}
export async function listContainers() {
  const r = await api.get<{ containers: Container[] }>('/containers')
  return r.data.containers
}
export type CreateContainerPayload = {
  image: string
  name?: string
  env?: string[]
  cmd?: string[]
  volume_binds?: string[]
  port_mappings?: PortMapping[]
  cpu_cores?: number
  memory_bytes?: number
  gpu_count?: number
  gpu_indices?: number[]
  node_id?: string
  pull?: boolean
  external_port?: number
}
export async function createContainer(payload: CreateContainerPayload) {
  return api.post('/containers', payload)
}
export async function deleteContainer(id: string) {
  return api.delete(`/containers/${id}`)
}
export async function startContainer(id: string) {
  return api.post(`/containers/${id}/start`)
}
export async function stopContainer(id: string) {
  return api.post(`/containers/${id}/stop`)
}
export async function restartContainer(id: string) {
  return api.post(`/containers/${id}/restart`)
}

/* ============================================================
   Formatters
   ============================================================ */

export function fmtBytes(n: number): string {
  if (n == null || isNaN(n)) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  if (n < 1024 ** 4) return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
  return `${(n / 1024 ** 4).toFixed(2)} TB`
}

export function fmtTime(iso?: string): string {
  if (!iso || iso.startsWith('0001') || iso.startsWith('1970')) return '—'
  return new Date(iso).toLocaleTimeString()
}

export function fmtDateTime(iso?: string): string {
  if (!iso || iso.startsWith('0001') || iso.startsWith('1970')) return '—'
  return new Date(iso).toLocaleString([], { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

export function fmtAge(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  let ms = Date.now() - d.getTime()
  if (ms < 0) ms = 0
  const s = Math.floor(ms / 1000)
  if (s < 60) return s + 's ago'
  const m = Math.floor(s / 60)
  if (m < 60) return m + 'm ago'
  const h = Math.floor(m / 60)
  if (h < 24) return h + 'h ago'
  const days = Math.floor(h / 24)
  if (days < 60) return days + 'd ago'
  const months = Math.floor(days / 30)
  if (months < 24) return months + 'mo ago'
  return Math.floor(months / 12) + 'y ago'
}

export function fmtUptime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  let ms = Date.now() - d.getTime()
  if (ms < 0) ms = 0
  const days = Math.floor(ms / 86400000)
  const hours = Math.floor((ms % 86400000) / 3600000)
  if (days >= 1) return days + 'd ' + hours + 'h'
  return hours + 'h'
}

/* ============================================================
   Resource label (composite tag for Containers tables)
   Returns an array of segments with a CSS class for coloring.
   ============================================================ */
export type ResourceSegment = { cls: string; text: string }
export function resourcesLabel(c: Container): ResourceSegment[] {
  const segs: ResourceSegment[] = []
  if (c.cpu_cores && c.cpu_cores > 0) segs.push({ cls: 'rt-cpu', text: `CPU ${c.cpu_cores}c` })
  if (c.memory_bytes && c.memory_bytes > 0) segs.push({ cls: 'rt-mem', text: fmtBytes(c.memory_bytes) })
  if (c.gpu_count && c.gpu_count > 0) {
    let text = `${c.gpu_count}×GPU`
    if (c.gpu_indices && c.gpu_indices.length) text += ' · ' + c.gpu_indices.join(', ')
    segs.push({ cls: 'rt-gpu', text })
  }
  if (segs.length === 0) segs.push({ cls: '', text: 'unlimited' })
  return segs
}

/* ============================================================
   State helpers — used by Containers tables and node status.
   The vocabulary is:
     online, offline           — node lifecycle
     starting, running,        — container transient + happy
     stopping, exited, error
   ============================================================ */
export function stateClass(state: string): string {
  if (state === 'online' || state === 'running') return 'is-success'
  if (state === 'offline' || state === 'exited') return ''
  if (state === 'starting') return 'is-accent'
  if (state === 'stopping') return 'is-warn'
  if (state === 'error') return 'is-danger'
  return ''
}

export function stateDot(state: string): string {
  return state
}
