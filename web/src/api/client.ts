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
  mem_used_bytes: number
  mem_total_bytes: number
  load1: number
  gpu_count: number
  gpu_used?: number
  gpu_free?: number
  gpu_mem_used_bytes: number
  gpu_mem_total_bytes: number
  gpu_usage_percent: number
  metrics_at: string
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

export function fmtBytes(n: number): string {
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
