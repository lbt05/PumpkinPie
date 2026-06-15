<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createContainer,
  fmtBytes,
  listNodeGPUs,
  listNodes,
  stateClass,
  type CreateContainerPayload,
  type Node,
  type NodeGPUDevice,
  type PortMapping,
} from '@/api/client'
import { useCreateHistory, type HistoryEntry } from '@/composables/useCreateHistory'

const router = useRouter()
const { t } = useI18n()
const nodes = ref<Node[]>([])

type GpuMode = 'auto' | 'pick'

const form = ref({
  image: 'nginx:alpine',
  name: '',
  cmd: '',
  envText: '',
  volumeBinds: '',
  ports: [{ container_port: 80, host_port: 0, protocol: 'tcp' }] as PortMapping[],
  cpuCores: 0,
  memoryMb: 0,
  gpuCount: 0,
  gpuMode: 'auto' as GpuMode,
  gpuIndices: [] as number[],
  nodeId: '' as string,
  pull: true,
})
const submitting = ref(false)
const result = ref<{ container: any; external_url?: string; node: { name: string }; gpu_indices?: number[] } | null>(null)
const gpuDevices = ref<NodeGPUDevice[]>([])
const loadingDevices = ref(false)
const pickedNode = ref<string>('')

const { entries: historyEntries, record: historyRecord, remove: historyRemove, clear: historyClear } = useCreateHistory()
// Open by default as soon as the user has at least one entry; first
// visit stays collapsed so the form starts clean.
const historyOpen = ref(historyEntries.value.length > 0)
const clearedToastShown = ref(false)

onMounted(async () => {
  try { nodes.value = await listNodes() } catch {}
})

const onlineNodes = computed(() => nodes.value.filter((n) => n.state === 'online'))
const gpuNodes = computed(() => onlineNodes.value.filter((n) => (n.gpu_count || 0) > 0))
const totalGpuFree = computed(() => onlineNodes.value.reduce((s, n) => s + (n.gpu_free || 0), 0))
const totalGpu = computed(() => onlineNodes.value.reduce((s, n) => s + (n.gpu_count || 0), 0))

const needsGpu = computed(() => form.value.gpuCount > 0 || form.value.gpuMode === 'pick')
const pickerNodes = computed(() => {
  if (!needsGpu.value) return onlineNodes.value
  if (form.value.gpuMode === 'auto' && form.value.gpuCount > 0) {
    return gpuNodes.value.filter((n) => (n.gpu_free ?? n.gpu_count) >= form.value.gpuCount)
  }
  return gpuNodes.value
})

function nodeOptionLabel(n: Node) {
  if ((n.gpu_count || 0) > 0) {
    return `${n.name} · ${n.hostname} · CPU ${Math.round(n.cpu_percent || 0)}% · ${n.gpu_free || 0}/${n.gpu_count} GPU free`
  }
  return `${n.name} · ${n.hostname} · CPU ${Math.round(n.cpu_percent || 0)}%`
}

const qualifyCount = computed(() => {
  if (form.value.gpuCount <= 0) return onlineNodes.value.length
  return gpuNodes.value.filter((n) => (n.gpu_free ?? n.gpu_count) >= form.value.gpuCount).length
})

const canSubmit = computed(() => {
  if (!form.value.image.trim()) return false
  if (!onlineNodes.value.length) return false
  if (form.value.gpuMode === 'pick' && (!form.value.nodeId || form.value.gpuIndices.length === 0)) return false
  return true
})

async function loadDevices() {
  form.value.gpuIndices = []
  gpuDevices.value = []
  if (form.value.gpuMode !== 'pick' || !form.value.nodeId) return
  loadingDevices.value = true
  try {
    gpuDevices.value = await listNodeGPUs(form.value.nodeId)
  } catch (e: any) {
    ElMessage({ type: 'error', message: t('createContainer.gpuLoadFailed', { msg: e?.response?.data?.error || e?.message || '' }) })
    gpuDevices.value = []
  } finally {
    loadingDevices.value = false
  }
}

watch(() => form.value.gpuMode, async (mode) => {
  if (mode === 'pick') {
    if (!form.value.nodeId && gpuNodes.value.length === 1) {
      form.value.nodeId = gpuNodes.value[0].id
      pickedNode.value = gpuNodes.value[0].id
    }
    await loadDevices()
  } else {
    form.value.gpuIndices = []
    gpuDevices.value = []
  }
})
watch(() => form.value.nodeId, async () => {
  pickedNode.value = form.value.nodeId
  if (form.value.gpuMode === 'pick') await loadDevices()
})
watch(() => form.value.gpuIndices.length, (n) => {
  if (form.value.gpuMode === 'pick') form.value.gpuCount = n
})

function toggleIndex(i: number, held: boolean) {
  if (held) return
  const arr = form.value.gpuIndices
  const at = arr.indexOf(i)
  if (at >= 0) arr.splice(at, 1)
  else arr.push(i)
  arr.sort((a, b) => a - b)
}

function addPort() { form.value.ports.push({ container_port: 80, host_port: 0, protocol: 'tcp' }) }
function removePort(i: number) { form.value.ports.splice(i, 1) }
function incPort(i: number) {
  form.value.ports[i].container_port = Math.min(65535, (form.value.ports[i].container_port || 0) + 1)
}
function decPort(i: number) {
  form.value.ports[i].container_port = Math.max(1, (form.value.ports[i].container_port || 1) - 1)
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  try {
    const payload: CreateContainerPayload = {
      image: form.value.image.trim(),
      name: form.value.name.trim() || undefined,
      cmd: form.value.cmd ? form.value.cmd.split(/\s+/).filter(Boolean) : undefined,
      env: form.value.envText ? form.value.envText.split(/\n+/).map((l) => l.trim()).filter(Boolean) : undefined,
      port_mappings: form.value.ports.filter((p) => p.container_port > 0),
      cpu_cores: form.value.cpuCores || 0,
      memory_bytes: form.value.memoryMb ? form.value.memoryMb * 1024 * 1024 : 0,
      gpu_count: form.value.gpuCount || 0,
      node_id: form.value.nodeId || undefined,
      pull: form.value.pull,
      volume_binds: form.value.volumeBinds ? form.value.volumeBinds.split(/\n+/).map((l) => l.trim()).filter(Boolean) : undefined,
    }
    if (form.value.gpuMode === 'pick' && form.value.gpuIndices.length > 0) {
      payload.gpu_indices = [...form.value.gpuIndices].sort((a, b) => a - b)
    }
    const r = await createContainer(payload)
    result.value = r.data
    historyRecord(payload, form.value.gpuMode)
    historyOpen.value = true
    ElMessage({ type: 'success', message: t('createContainer.success') + ' — ' + r.data.node.name })
    // Smooth-scroll to result
    setTimeout(() => {
      const el = document.getElementById('result-wrap')
      if (el) {
        const top = el.getBoundingClientRect().top
        window.scrollTo({ top: window.scrollY + top - 80, behavior: 'smooth' })
      }
    }, 50)
  } catch (e: any) {
    ElMessage({ type: 'error', message: t('createContainer.failed', { msg: e?.response?.data?.error || e?.message || 'unknown error' }) })
  } finally {
    submitting.value = false
  }
}

function reset() {
  result.value = null
  form.value.name = ''
  form.value.cmd = ''
  form.value.envText = ''
  form.value.volumeBinds = ''
  form.value.gpuIndices = []
  ElMessage({ type: 'info', message: t('createContainer.toastResetMsg') })
}

// ---- History ----

function applyFromHistory(e: HistoryEntry) {
  const p = e.payload
  // Drop the stale success card so the user isn't confused by a
  // banner that no longer matches the form.
  result.value = null
  form.value.image = p.image
  form.value.name = p.name ?? ''
  form.value.cmd = (p.cmd ?? []).join(' ')
  form.value.envText = (p.env ?? []).join('\n')
  form.value.volumeBinds = (p.volume_binds ?? []).join('\n')
  form.value.ports = (p.port_mappings ?? []).length
    ? p.port_mappings!.map((m) => ({ ...m }))
    : [{ container_port: 80, host_port: 0, protocol: 'tcp' }]
  form.value.cpuCores = p.cpu_cores ?? 0
  form.value.memoryMb = p.memory_bytes ? Math.round(p.memory_bytes / (1024 * 1024)) : 0
  form.value.gpuCount = p.gpu_count ?? 0
  // Prefer the saved gpuMode; fall back to 'auto' for entries
  // written before the field existed, and to 'auto' when no GPUs
  // were requested.
  const savedMode = e.gpuMode
  form.value.gpuMode = savedMode && p.gpu_count && p.gpu_count > 0 ? savedMode : 'auto'
  form.value.pull = !!p.pull
  // gpuIndices are ephemeral: a device that was free when this
  // recipe was saved may be held now. Let the user re-pick.
  form.value.gpuIndices = []
  // Pin a node only if it's still in the cluster; otherwise drop
  // the pin and tell the user why.
  if (p.node_id && nodes.value.some((n) => n.id === p.node_id)) {
    form.value.nodeId = p.node_id
  } else {
    const hadPin = !!p.node_id
    form.value.nodeId = ''
    if (hadPin) {
      ElMessage({ type: 'warning', message: t('createContainer.historyNodeDropped') })
    }
  }
  ElMessage({ type: 'success', message: t('createContainer.historyApplied') })
}

async function confirmClearHistory() {
  try {
    await ElMessageBox.confirm(
      t('createContainer.historyClearConfirmMsg'),
      t('createContainer.historyClearConfirmTitle'),
      {
        type: 'warning',
        confirmButtonText: t('createContainer.historyClearAll'),
        cancelButtonText: t('common.cancel'),
      },
    )
  } catch {
    return
  }
  historyClear()
  if (!clearedToastShown.value) {
    clearedToastShown.value = true
    ElMessage({ type: 'info', message: t('createContainer.historyCleared') })
  }
}

function fmtAgo(ts: number): string {
  const diff = Date.now() - ts
  if (diff < 60_000) return t('createContainer.historyTimeJustNow')
  const m = Math.floor(diff / 60_000)
  if (m < 60) return t('createContainer.historyTimeAgoMin', { n: m })
  const h = Math.floor(m / 60)
  if (h < 24) return t('createContainer.historyTimeAgoHour', { n: h })
  const d = Math.floor(h / 24)
  return t('createContainer.historyTimeAgoDay', { n: d })
}

const sumImage = computed(() => form.value.image.trim() || '—')
const sumName = computed(() => form.value.name.trim() || t('createContainer.summaryAuto'))
const sumPorts = computed(() => String(form.value.ports.length))
const sumCpu = computed(() => form.value.cpuCores > 0 ? `${form.value.cpuCores} ${t('common.cores')}` : t('common.unlimited'))
const sumMem = computed(() => form.value.memoryMb > 0 ? `${form.value.memoryMb} MB` : t('common.unlimited'))
const sumGpu = computed(() => {
  if (form.value.gpuCount === 0) return '0'
  if (form.value.gpuMode === 'pick' && form.value.gpuIndices.length) {
    return `${form.value.gpuCount} · GPU ${form.value.gpuIndices.join(', ')}`
  }
  return String(form.value.gpuCount)
})
const sumNode = computed(() => {
  if (!form.value.nodeId) return t('createContainer.summaryAuto')
  const n = nodes.value.find((x) => x.id === form.value.nodeId)
  return n ? n.name : t('createContainer.summaryAuto')
})
const sumPull = computed(() => form.value.pull ? 'yes' : 'no')

const schedHint = computed(() => {
  if (form.value.gpuMode === 'pick' && !pickedNode.value) {
    return { text: t('createContainer.schedPickNode'), warn: true }
  }
  if (form.value.gpuCount > 0 && form.value.gpuMode === 'auto' && !form.value.nodeId) {
    return { text: t('createContainer.schedPinHint'), warn: true }
  }
  return { text: t('createContainer.schedEmpty'), warn: false }
})

const gpuAutoHint = computed(() => {
  if (form.value.gpuCount === 0) return { text: t('createContainer.gpuAutoZero'), warn: false }
  const qualify = gpuNodes.value.filter((n) => (n.gpu_free ?? n.gpu_count) >= form.value.gpuCount).length
  const s = form.value.gpuCount !== 1 ? 's' : ''
  return {
    text: t('createContainer.gpuAutoQualify', { n: qualify, total: onlineNodes.value.length, count: form.value.gpuCount, s }),
    warn: qualify === 0,
  }
})
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('page.newContainer.title') }}</h1>
        <p class="page-subtitle">
          <span>{{ t('createContainer.subtitle') }}</span>
          <span class="dot" />
          <span>{{ t('createContainer.subtitleHint') }}</span>
        </p>
      </div>
      <div class="page-actions">
        <button class="btn is-ghost" type="button" @click="router.push('/containers')">{{ t('createContainer.viewAll') }}</button>
      </div>
    </header>

    <div v-if="!onlineNodes.length" class="empty">
      <div class="ico" style="color: var(--danger); border-color: var(--danger);" aria-hidden="true">!</div>
      <p class="empty-title" style="color: var(--danger);">{{ t('createContainer.noOnlineNodes') }}</p>
      <p class="empty-hint">{{ t('createContainer.noOnlineNodesHint') }}</p>
    </div>

    <div v-else class="create-layout">
      <!-- ============ Form ============ -->
      <form class="create-form" autocomplete="off" @submit.prevent="submit">
        <!-- ----- Image & config ----- -->
        <section class="form-section">
          <div class="field">
            <label class="field-label" for="f-image">
              {{ t('createContainer.fieldImage') }} <span class="opt">{{ t('createContainer.fieldImageRequired') }}</span>
            </label>
            <input class="input mono" id="f-image" v-model="form.image" type="text" :placeholder="t('createContainer.fieldImagePlaceholder')" />
            <p class="field-hint">{{ t('createContainer.fieldImageHint') }}</p>
          </div>

          <div class="field-row">
            <div class="field" style="flex: 1 1 220px;">
              <label class="field-label" for="f-name">
                {{ t('createContainer.fieldName') }} <span class="opt">{{ t('createContainer.fieldNameOptional') }}</span>
              </label>
              <input class="input" id="f-name" v-model="form.name" type="text" :placeholder="t('createContainer.fieldNamePlaceholder')" />
            </div>
            <div class="field" style="flex: 1 1 220px;">
              <label class="field-label" for="f-cmd">
                {{ t('createContainer.fieldCmd') }} <span class="opt">{{ t('createContainer.fieldNameOptional') }}</span>
              </label>
              <input class="input mono" id="f-cmd" v-model="form.cmd" type="text" :placeholder="t('createContainer.fieldCmdPlaceholder')" />
            </div>
          </div>

          <div class="field">
            <label class="field-label" for="f-env">
              {{ t('createContainer.fieldEnv') }} <span class="opt">{{ t('createContainer.fieldEnvHint') }}</span>
            </label>
            <textarea class="textarea" id="f-env" v-model="form.envText" rows="3" :placeholder="t('createContainer.fieldEnvPlaceholder')" />
          </div>

          <div class="field">
            <label class="field-label" for="f-vol">
              {{ t('createContainer.fieldVolumes') }} <span class="opt">{{ t('createContainer.fieldVolumesHint') }}</span>
            </label>
            <textarea class="textarea" id="f-vol" v-model="form.volumeBinds" rows="2" :placeholder="t('createContainer.fieldVolumesPlaceholder')" />
          </div>

          <div class="field">
            <label class="field-label">{{ t('createContainer.fieldPorts') }}</label>
            <div class="col" style="gap: var(--sp-2);">
              <div v-for="(p, i) in form.ports" :key="i" class="field-row">
                <div class="numstep" style="width: 140px;">
                  <button type="button" @click="decPort(i)">−</button>
                  <input v-model.number="p.container_port" type="number" min="1" max="65535" step="1" :title="t('createContainer.portContainerTitle')" />
                  <button type="button" @click="incPort(i)">＋</button>
                </div>
                <span class="arrow" aria-hidden="true">→</span>
                <div class="numstep" style="width: 140px;">
                  <input v-model.number="p.host_port" type="number" min="0" max="65535" step="1" :placeholder="String(p.container_port || '')" :title="t('createContainer.portHostTitle')" />
                </div>
                <select class="select" v-model="p.protocol" style="max-width: 120px;">
                  <option value="tcp">tcp</option>
                  <option value="udp">udp</option>
                </select>
                <button class="btn is-danger is-small" type="button" @click="removePort(i)" :disabled="form.ports.length <= 1">{{ t('act.delete') }}</button>
              </div>
            </div>
            <button class="btn is-ghost is-small" type="button" style="align-self: flex-start; margin-top: var(--sp-2);" @click="addPort">
              <span class="ico" aria-hidden="true">＋</span> {{ t('createContainer.addPort') }}
            </button>
            <p class="field-hint">{{ t('createContainer.portHint') }}</p>
          </div>
        </section>

        <!-- ----- Resources ----- -->
        <h3 class="form-divider">{{ t('createContainer.sectionResources') }}</h3>
        <section class="form-section">
          <div class="field-row">
            <div class="field">
              <label class="field-label" for="f-cpu">{{ t('createContainer.fieldCpu') }}</label>
              <el-input-number v-model="form.cpuCores" :min="0" :step="0.5" :precision="1" style="width:160px;" />
              <p class="field-hint"><code class="code">0</code> = {{ t('createContainer.fieldCpuHint').replace('0 = ', '') }}</p>
            </div>
            <div class="field">
              <label class="field-label" for="f-mem">{{ t('createContainer.fieldMemory') }}</label>
              <el-input-number v-model="form.memoryMb" :min="0" :step="64" style="width:160px;" />
              <p class="field-hint">{{ t('createContainer.fieldMemoryHint') }}</p>
            </div>
            <div class="field">
              <label class="field-label">{{ t('createContainer.fieldPull') }}</label>
              <label class="row-tight" style="cursor:pointer;">
                <span class="switch">
                  <input v-model="form.pull" type="checkbox" />
                  <span class="track" />
                </span>
                <span class="field-hint" style="margin: 0;">{{ t('createContainer.fieldPullHint') }}</span>
              </label>
            </div>
          </div>
        </section>

        <!-- ----- GPU ----- -->
        <h3 class="form-divider">{{ t('createContainer.sectionGpu') }}</h3>
        <section class="form-section">
          <p class="gpu-summary">
            <span class="ico" aria-hidden="true">▦</span>
            <span><strong>{{ totalGpuFree }}</strong> / <strong>{{ totalGpu }}</strong> {{ t('createContainer.clusterGpuSummary', { free: totalGpuFree, total: totalGpu, nodes: gpuNodes.length }) }}</span>
          </p>

          <div class="field">
            <label class="field-label">{{ t('createContainer.gpuMode') }}</label>
            <div class="segmented" role="tablist">
              <button type="button" :class="{ 'is-active': form.gpuMode === 'auto', 'is-accent': form.gpuMode === 'auto' }" role="tab" :aria-selected="form.gpuMode === 'auto'" @click="form.gpuMode = 'auto'">{{ t('createContainer.gpuModeAuto') }}</button>
              <button type="button" :class="{ 'is-active': form.gpuMode === 'pick', 'is-accent': form.gpuMode === 'pick' }" role="tab" :aria-selected="form.gpuMode === 'pick'" @click="form.gpuMode = 'pick'">{{ t('createContainer.gpuModePick') }}</button>
            </div>
          </div>

          <!-- AUTO -->
          <div v-if="form.gpuMode === 'auto'" class="field">
            <label class="field-label" for="f-gpu-count">{{ t('createContainer.fieldGpu') }}</label>
            <el-input-number v-model="form.gpuCount" :min="0" :max="64" :step="1" style="width:140px;" />
            <p :class="['field-hint', { 'is-warn': gpuAutoHint.warn }]" style="margin-top: var(--sp-2);">
              <template v-if="gpuAutoHint.warn">
                <strong>{{ qualifyCount }}</strong> of {{ onlineNodes.length }} — {{ t('createContainer.gpuNoCapable') }}
              </template>
              <template v-else>
                {{ gpuAutoHint.text }}
              </template>
            </p>
          </div>

          <!-- PICK -->
          <div v-else class="field">
            <p class="field-hint">{{ t('createContainer.gpuPickHint') }}</p>
            <div v-if="!form.nodeId" class="empty" style="grid-column: 1/-1; padding: var(--sp-6);">
              <div class="ico" aria-hidden="true">▦</div>
              <p class="empty-title">{{ t('createContainer.gpuPickNeedNode') }}</p>
              <p class="empty-hint">{{ t('createContainer.gpuPickNeedNodeHint') }}</p>
            </div>
            <div v-else-if="loadingDevices" class="field-hint" style="margin-top: var(--sp-2);">…</div>
            <div v-else-if="!gpuDevices.length" class="empty" style="grid-column: 1/-1; padding: var(--sp-6);">
              <div class="ico" aria-hidden="true">▦</div>
              <p class="empty-title">{{ t('createContainer.gpuNoDevices') }}</p>
              <p class="empty-hint">{{ t('createContainer.gpuNoDevicesHint') }}</p>
            </div>
            <div v-else class="gpu-grid">
              <div
                v-for="d in gpuDevices"
                :key="d.index"
                :class="['gpu-card', { 'is-selected': form.gpuIndices.includes(d.index), 'is-held': !!d.held_by }]"
                :aria-disabled="!!d.held_by"
                :tabindex="d.held_by ? -1 : 0"
                @click="toggleIndex(d.index, !!d.held_by)"
                @keydown.enter.prevent="toggleIndex(d.index, !!d.held_by)"
                @keydown.space.prevent="toggleIndex(d.index, !!d.held_by)"
              >
                <div class="gpu-card-head">
                  <span class="gpu-card-name">GPU {{ d.index }}</span>
                  <span v-if="d.held_by" class="badge is-danger">{{ t('createContainer.gpuInUse') }}</span>
                  <span v-else-if="form.gpuIndices.includes(d.index)" class="badge is-accent">{{ t('createContainer.gpuSelected') }}</span>
                  <span v-else class="badge is-success">{{ t('createContainer.gpuFreePill') }}</span>
                </div>
                <span v-if="d.name" class="gpu-card-device">{{ d.name }}</span>
                <span v-if="d.mem_total_bytes" class="gpu-card-mem">{{ fmtBytes(d.mem_total_bytes) }}</span>
              </div>
            </div>
          </div>
        </section>

        <!-- ----- Scheduling ----- -->
        <h3 class="form-divider">{{ t('createContainer.sectionScheduling') }}</h3>
        <section class="form-section">
          <div class="field">
            <label class="field-label" for="f-node">
              {{ t('createContainer.fieldTargetNode') }} <span class="opt">{{ t('createContainer.fieldTargetNodeOptional') }}</span>
            </label>
            <select class="select" id="f-node" v-model="form.nodeId" :disabled="form.gpuMode === 'pick' && !form.nodeId && gpuNodes.length === 0">
              <option value="">{{ t('createContainer.schedAuto') }}</option>
              <option v-for="n in pickerNodes" :key="n.id" :value="n.id">{{ nodeOptionLabel(n) }}</option>
            </select>
            <p :class="['field-hint', { 'is-warn': schedHint.warn }]" style="margin-top: var(--sp-2);">
              {{ schedHint.text }}
            </p>
          </div>
        </section>
      </form>

      <!-- ============ Sticky side ============ -->
      <aside class="create-side">
        <div class="create-summary">
          <h3>{{ t('createContainer.summaryTitle') }}</h3>
          <ul class="summary-list">
            <li><span>{{ t('createContainer.summaryImage') }}</span><span class="ellipsis">{{ sumImage }}</span></li>
            <li><span>{{ t('createContainer.summaryName') }}</span><span class="ellipsis">{{ sumName }}</span></li>
            <li><span>{{ t('createContainer.summaryPorts') }}</span><span>{{ sumPorts }}</span></li>
            <li><span>{{ t('createContainer.summaryCpu') }}</span><span>{{ sumCpu }}</span></li>
            <li><span>{{ t('createContainer.summaryMem') }}</span><span>{{ sumMem }}</span></li>
            <li><span>{{ t('createContainer.summaryGpu') }}</span><span>{{ sumGpu }}</span></li>
            <li><span>{{ t('createContainer.summaryNode') }}</span><span class="ellipsis">{{ sumNode }}</span></li>
            <li><span>{{ t('createContainer.summaryPull') }}</span><span>{{ sumPull }}</span></li>
          </ul>
        </div>

        <div class="create-history">
          <button class="history-head" type="button" :aria-expanded="historyOpen" @click="historyOpen = !historyOpen">
            <span class="caret" aria-hidden="true">{{ historyOpen ? '▾' : '▸' }}</span>
            <h3>{{ t('createContainer.historyTitle') }}</h3>
            <span v-if="historyEntries.length" class="count-pill">{{ historyEntries.length }}</span>
          </button>
          <ul v-if="historyOpen" class="history-list">
            <li v-if="!historyEntries.length" class="history-empty">{{ t('createContainer.historyEmpty') }}</li>
            <li v-for="e in historyEntries" :key="e.id" class="history-item" :title="e.label">
              <button class="history-row" type="button" @click="applyFromHistory(e)">
                <span class="history-label ellipsis">{{ e.label }}</span>
                <span class="history-time">{{ fmtAgo(e.ts) }}</span>
              </button>
              <button class="history-x" type="button" :title="t('createContainer.historyRemove')" @click.stop="historyRemove(e.id)">×</button>
            </li>
          </ul>
          <button v-if="historyOpen && historyEntries.length" class="btn is-ghost is-small history-clear" type="button" @click="confirmClearHistory">
            {{ t('createContainer.historyClearAll') }}
          </button>
        </div>

        <div class="sticky-actions">
          <button class="btn is-primary" type="button" :disabled="!canSubmit || submitting" :loading="submitting" style="width: 100%; justify-content: center;" @click="submit">
            <span class="ico" aria-hidden="true">✦</span>
            <span>{{ t('createContainer.buttonCreate') }}</span>
          </button>
          <button class="btn is-ghost" type="button" style="width: 100%; justify-content: center;" @click="reset">{{ t('createContainer.buttonReset') }}</button>
        </div>
      </aside>
    </div>

    <!-- ============ Result card ============ -->
    <div v-if="result" id="result-wrap" style="margin-top: var(--sp-7);">
      <article class="result">
        <div class="result-head">
          <span class="ico" aria-hidden="true">✓</span>
          <h2 class="result-title">{{ t('createContainer.success') }}</h2>
          <span class="badge" :class="stateClass(result.container.state)" style="margin-left: auto;">{{ t('state.' + result.container.state, result.container.state) }}</span>
        </div>
        <p class="result-hint">{{ t('createContainer.resultHint') }}</p>
        <dl class="descriptions">
          <dt>{{ t('createContainer.resultContainerId') }}</dt><dd class="mono">{{ result.container.id }}</dd>
          <dt>{{ t('createContainer.resultNode') }}</dt><dd class="mono">{{ result.node.name }}</dd>
          <dt>{{ t('createContainer.resultImage') }}</dt><dd class="mono col-ellipsis">{{ result.container.image }}</dd>
          <dt>{{ t('createContainer.resultState') }}</dt>
          <dd>
            <span class="status-dot" :class="result.container.state" />
            <span class="badge" :class="stateClass(result.container.state)" style="margin-left:6px;">{{ t('state.' + result.container.state, result.container.state) }}</span>
          </dd>
          <template v-if="result.gpu_indices && result.gpu_indices.length">
            <dt>{{ t('createContainer.resultGpuIndices') }}</dt>
            <dd class="mono">{{ result.gpu_indices.length }} · GPU {{ result.gpu_indices.join(', ') }}</dd>
          </template>
          <template v-if="result.external_url">
            <dt>{{ t('createContainer.resultPublicUrl') }}</dt>
            <dd><a :href="result.external_url" target="_blank" rel="noopener">{{ result.external_url }}</a></dd>
          </template>
        </dl>
      </article>
    </div>
  </div>
</template>

<style scoped>
.gpu-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: var(--sp-3);
  margin-top: var(--sp-2);
}
</style>
