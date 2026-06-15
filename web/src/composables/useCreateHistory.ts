import { ref, watch } from 'vue'
import type { CreateContainerPayload } from '@/api/client'

// localStorage key — versioned so a payload-shape change can
// invalidate old entries without colliding with the new format.
const STORAGE_KEY = 'pp.createHistory.v1'
const MAX_ENTRIES = 20

export type HistoryEntry = {
  id: string
  ts: number
  label: string
  payload: CreateContainerPayload
  // gpuMode is form-side state (not part of the API payload) but we
  // keep it on the entry so an Apply can put the user back in
  // 'pick' mode if that's what they used originally.
  gpuMode?: 'auto' | 'pick'
}

function safeRead(): HistoryEntry[] {
  if (typeof localStorage === 'undefined') return []
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    // Defensive: drop anything that doesn't look like an entry. Keeps
    // a corrupt write from poisoning the rest of the list.
    return parsed.filter(
      (e: any) =>
        e &&
        typeof e.id === 'string' &&
        typeof e.ts === 'number' &&
        typeof e.label === 'string' &&
        e.payload && typeof e.payload === 'object',
    )
  } catch {
    return []
  }
}

function safeWrite(entries: HistoryEntry[]) {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(entries))
  } catch {
    // Quota / private-mode — silently drop. The in-memory state
    // is still correct for this tab.
  }
}

// Stable JSON key ordering for de-dup hashing. We rebuild the object
// in a fixed field order so two structurally-identical payloads
// produce the same string regardless of insertion order.
function stableStringify(p: CreateContainerPayload): string {
  const ordered: Record<string, unknown> = {
    image: p.image ?? '',
    name: p.name ?? '',
    cmd: p.cmd ? [...p.cmd].sort() : [],
    env: p.env ? [...p.env].sort() : [],
    volume_binds: p.volume_binds ? [...p.volume_binds].sort() : [],
    port_mappings: (p.port_mappings ?? [])
      .map((pm) => `${pm.container_port}/${pm.protocol}/${pm.host_port ?? 0}`)
      .sort(),
    cpu_cores: p.cpu_cores ?? 0,
    memory_bytes: p.memory_bytes ?? 0,
    gpu_count: p.gpu_count ?? 0,
    node_id: p.node_id ?? '',
    pull: !!p.pull,
  }
  return JSON.stringify(ordered)
}

function makeId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `h-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

// Compute a short, scannable label for the list row. Kept here
// (not in the view) so it stays consistent no matter who renders.
export function historyLabel(p: CreateContainerPayload): string {
  const parts: string[] = [p.image || '—']
  const ports = p.port_mappings?.filter((m) => m.container_port > 0).length ?? 0
  parts.push(ports === 1 ? '1 port' : `${ports} ports`)
  if (p.gpu_count && p.gpu_count > 0) {
    parts.push(`${p.gpu_count} GPU${p.gpu_count > 1 ? 's' : ''}`)
  }
  if (p.cpu_cores && p.cpu_cores > 0) parts.push(`${p.cpu_cores} CPU`)
  if (p.memory_bytes && p.memory_bytes > 0) {
    const mb = Math.round(p.memory_bytes / (1024 * 1024))
    parts.push(`${mb} MB`)
  }
  return parts.join(' · ')
}

const entries = ref<HistoryEntry[]>(safeRead())

// Persist on any mutation. `deep: true` isn't needed because we
// always reassign the array.
watch(entries, (v) => safeWrite(v), { deep: true })

function findByPayload(p: CreateContainerPayload): string | null {
  const fp = stableStringify(p)
  const hit = entries.value.find((e) => stableStringify(e.payload) === fp)
  return hit ? hit.id : null
}

function record(p: CreateContainerPayload, gpuMode?: 'auto' | 'pick') {
  const fp = stableStringify(p)
  // Drop existing entry with the same payload (if any), then prepend
  // the fresh one so it jumps to the top of the list.
  entries.value = [
    { id: makeId(), ts: Date.now(), label: historyLabel(p), payload: p, gpuMode },
    ...entries.value.filter((e) => stableStringify(e.payload) !== fp),
  ].slice(0, MAX_ENTRIES)
  // Suppress the unused-binding warning; fp could be useful in
  // future telemetry hooks.
  void fp
}

function remove(id: string) {
  entries.value = entries.value.filter((e) => e.id !== id)
}

function clear() {
  entries.value = []
}

export function useCreateHistory() {
  return { entries, record, remove, clear, findByPayload }
}
