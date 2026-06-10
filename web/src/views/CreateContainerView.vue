<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  createContainer,
  listNodes,
  listNodeGPUs,
  type CreateContainerPayload,
  type Node,
  type NodeGPUDevice,
  type PortMapping,
  fmtBytes,
} from '@/api/client'

const router = useRouter()
const { t } = useI18n()
const nodes = ref<Node[]>([])

type GpuMode = 'auto' | 'pick'

const form = ref({
  image: 'nginx:alpine',
  name: '',
  cmd: '',
  envText: '',
  ports: [{ container_port: 80, protocol: 'tcp' }] as PortMapping[],
  cpu_cores: 0,
  memory_mb: 0,
  gpu_count: 0,
  gpu_mode: 'auto' as GpuMode,
  gpu_indices: [] as number[],
  node_id: '' as string,
  pull: true,
  volume_binds: '',
})

const submitting = ref(false)
const result = ref<{ container: any; external_url?: string; node: { name: string }; gpu_indices?: number[] } | null>(null)
const gpuDevices = ref<NodeGPUDevice[]>([])
const loadingDevices = ref(false)

onMounted(async () => {
  try {
    nodes.value = await listNodes()
  } catch (e) {
    console.warn(e)
  }
})

const onlineNodes = computed(() => nodes.value.filter((n) => n.state === 'online'))

const clusterGpuTotal = computed(() => onlineNodes.value.reduce((s, n) => s + (n.gpu_count || 0), 0))
const clusterGpuFree = computed(() =>
  onlineNodes.value.reduce((s, n) => s + ((n.gpu_free ?? n.gpu_count) || 0), 0),
)
const gpuCapableNodes = computed(() => onlineNodes.value.filter((n) => (n.gpu_count || 0) > 0))

const needsGpu = computed(() => form.value.gpu_count > 0 || form.value.gpu_mode === 'pick')

// The list shown in the node picker. When the user wants GPUs we filter to
// GPU-capable nodes (and additionally to those with enough free GPUs when
// in auto mode and a count is set).
const pickerNodes = computed(() => {
  if (!needsGpu.value) return onlineNodes.value
  const capable = gpuCapableNodes.value
  if (form.value.gpu_mode === 'auto' && form.value.gpu_count > 0) {
    return capable.filter((n) => (n.gpu_free ?? n.gpu_count) >= form.value.gpu_count)
  }
  return capable
})

const satisfyingNodeCount = computed(() => {
  if (form.value.gpu_count <= 0) return onlineNodes.value.length
  return gpuCapableNodes.value.filter((n) => (n.gpu_free ?? n.gpu_count) >= form.value.gpu_count).length
})

function nodeLabel(n: Node) {
  if ((n.gpu_count || 0) > 0) {
    return t('createContainer.nodeOptionGpu', {
      name: n.name,
      host: n.hostname,
      cpu: n.cpu_percent.toFixed(0),
      free: n.gpu_free ?? n.gpu_count,
      total: n.gpu_count,
    })
  }
  return t('createContainer.nodeOption', { name: n.name, host: n.hostname, cpu: n.cpu_percent.toFixed(0) })
}

const canSubmit = computed(() => {
  if (!form.value.image) return false
  if (onlineNodes.value.length === 0) return false
  if (form.value.gpu_mode === 'pick') {
    return !!form.value.node_id && form.value.gpu_indices.length > 0
  }
  return true
})

async function loadDevices() {
  form.value.gpu_indices = []
  gpuDevices.value = []
  if (form.value.gpu_mode !== 'pick' || !form.value.node_id) return
  loadingDevices.value = true
  try {
    gpuDevices.value = await listNodeGPUs(form.value.node_id)
  } catch (e: any) {
    ElMessage.error(t('createContainer.gpuLoadFailed', { msg: e?.response?.data?.error || e?.message || '' }))
    gpuDevices.value = []
  } finally {
    loadingDevices.value = false
  }
}

watch(() => form.value.gpu_mode, async (mode) => {
  if (mode === 'pick') {
    if (!form.value.node_id && gpuCapableNodes.value.length === 1) {
      form.value.node_id = gpuCapableNodes.value[0].id
    }
    await loadDevices()
  } else {
    form.value.gpu_indices = []
    gpuDevices.value = []
  }
})

watch(() => form.value.node_id, async () => {
  if (form.value.gpu_mode === 'pick') await loadDevices()
})

watch(() => form.value.gpu_indices.length, (n) => {
  if (form.value.gpu_mode === 'pick') form.value.gpu_count = n
})

function toggleIndex(i: number, disabled: boolean) {
  if (disabled) return
  const arr = form.value.gpu_indices
  const at = arr.indexOf(i)
  if (at >= 0) arr.splice(at, 1)
  else arr.push(i)
}

function addPort() {
  form.value.ports.push({ container_port: 80, protocol: 'tcp' })
}
function removePort(i: number) {
  form.value.ports.splice(i, 1)
}

async function submit() {
  if (!canSubmit.value) return
  submitting.value = true
  try {
    const payload: CreateContainerPayload = {
      image: form.value.image.trim(),
      name: form.value.name.trim() || undefined,
      cmd: form.value.cmd
        ? form.value.cmd.split(/\s+/).filter(Boolean)
        : undefined,
      env: form.value.envText
        ? form.value.envText.split(/\n+/).map((l) => l.trim()).filter(Boolean)
        : undefined,
      port_mappings: form.value.ports.filter((p) => p.container_port > 0),
      cpu_cores: form.value.cpu_cores || 0,
      memory_bytes: form.value.memory_mb ? form.value.memory_mb * 1024 * 1024 : 0,
      gpu_count: form.value.gpu_count || 0,
      node_id: form.value.node_id || undefined,
      pull: form.value.pull,
      volume_binds: form.value.volume_binds
        ? form.value.volume_binds.split(/\n+/).map((l) => l.trim()).filter(Boolean)
        : undefined,
    }
    if (form.value.gpu_mode === 'pick' && form.value.gpu_indices.length > 0) {
      payload.gpu_indices = [...form.value.gpu_indices].sort((a, b) => a - b)
    }
    const r = await createContainer(payload)
    result.value = r.data
    ElMessage.success(t('createContainer.success', { node: r.data.node.name }))
  } catch (e: any) {
    const msg = e?.response?.data?.error || e?.message || 'unknown error'
    ElMessage.error(t('createContainer.failed', { msg }))
  } finally {
    submitting.value = false
  }
}

function reset() {
  result.value = null
  form.value.name = ''
  form.value.cmd = ''
  form.value.envText = ''
  form.value.volume_binds = ''
  form.value.gpu_indices = []
}

function stateLabel(s: string) {
  return t(`state.${s}` as any, s)
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('createContainer.title') }}</h1>
        <div class="page-subtitle">{{ t('createContainer.subtitle') }}</div>
      </div>
    </div>

    <div v-if="!onlineNodes.length" style="text-align:center;color:var(--text-dim);padding:48px 0;">
      <el-icon :size="48" color="var(--red)"><WarningFilled /></el-icon>
      <div style="margin-top:12px;">{{ t('createContainer.noOnlineNodes') }}</div>
      <div style="margin-top:8px;font-size:12px;">{{ t('createContainer.noOnlineNodesHint') }}</div>
    </div>

    <el-card v-else>
      <el-form label-position="top" :model="form" v-loading="submitting">
        <el-row :gutter="20">
          <el-col :span="14">
            <el-form-item :label="t('createContainer.fieldImage')" required>
              <el-input v-model="form.image" :placeholder="t('createContainer.fieldImagePlaceholder')" />
            </el-form-item>
          </el-col>
          <el-col :span="10">
            <el-form-item :label="t('createContainer.fieldName')">
              <el-input v-model="form.name" :placeholder="t('createContainer.fieldNamePlaceholder')" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-form-item :label="t('createContainer.fieldCmd')">
          <el-input v-model="form.cmd" :placeholder="t('createContainer.fieldCmdPlaceholder')" />
        </el-form-item>

        <el-form-item :label="t('createContainer.fieldEnv')">
          <el-input
            v-model="form.envText"
            type="textarea"
            :rows="3"
            :placeholder="t('createContainer.fieldEnvPlaceholder')"
          />
        </el-form-item>

        <el-form-item :label="t('createContainer.fieldVolumes')">
          <el-input
            v-model="form.volume_binds"
            type="textarea"
            :rows="2"
            :placeholder="t('createContainer.fieldVolumesPlaceholder')"
          />
        </el-form-item>

        <el-form-item :label="t('createContainer.fieldPorts')">
          <div style="width:100%;">
            <div v-for="(p, i) in form.ports" :key="i" style="display:flex;gap:8px;margin-bottom:6px;">
              <el-input-number v-model="p.container_port" :min="1" :max="65535" />
              <el-select v-model="p.protocol" style="width:120px;">
                <el-option label="tcp" value="tcp" />
                <el-option label="udp" value="udp" />
              </el-select>
              <el-button icon="Delete" circle @click="removePort(i)" :disabled="form.ports.length<=1" />
            </div>
            <el-button icon="CirclePlus" size="small" @click="addPort">{{ t('createContainer.addPort') }}</el-button>
            <span style="margin-left:12px;font-size:12px;color:var(--text-dim);">
              {{ t('createContainer.portHint') }}
            </span>
          </div>
        </el-form-item>

        <el-divider>{{ t('createContainer.dividerResources') }}</el-divider>

        <el-row :gutter="20">
          <el-col :span="8">
            <el-form-item :label="t('createContainer.fieldCpu')">
              <el-input-number v-model="form.cpu_cores" :min="0" :step="0.5" :precision="1" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item :label="t('createContainer.fieldMemory')">
              <el-input-number v-model="form.memory_mb" :min="0" :step="64" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item :label="t('createContainer.fieldPull')">
              <el-switch v-model="form.pull" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-divider>{{ t('createContainer.dividerGpu') }}</el-divider>

        <div style="font-size:12px;color:var(--text-dim);margin-bottom:10px;">
          {{ t('createContainer.clusterGpuSummary', {
            free: clusterGpuFree,
            total: clusterGpuTotal,
            nodes: gpuCapableNodes.length,
          }) }}
        </div>

        <el-form-item :label="t('createContainer.gpuMode')">
          <el-radio-group v-model="form.gpu_mode">
            <el-radio-button label="auto">{{ t('createContainer.gpuModeAuto') }}</el-radio-button>
            <el-radio-button label="pick">{{ t('createContainer.gpuModePick') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <el-form-item v-if="form.gpu_mode === 'auto'" :label="t('createContainer.fieldGpu')">
          <el-input-number v-model="form.gpu_count" :min="0" :max="64" style="width:200px;" />
          <span v-if="form.gpu_count > 0" style="margin-left:12px;font-size:12px;color:var(--text-dim);">
            {{ t('createContainer.gpuSatisfyingNodes', {
              n: satisfyingNodeCount,
              total: onlineNodes.length,
              count: form.gpu_count,
            }) }}
          </span>
        </el-form-item>

        <el-form-item v-else :label="t('createContainer.gpuPickHint')">
          <div v-if="!form.node_id" style="font-size:12px;color:var(--accent);">
            <el-icon><WarningFilled /></el-icon>
            {{ t('createContainer.gpuPickNeedNode') }}
          </div>
          <div v-else-if="loadingDevices" style="font-size:12px;color:var(--text-dim);">
            {{ t('createContainer.gpuLoading') }}
          </div>
          <div v-else-if="!gpuDevices.length" style="font-size:12px;color:var(--text-dim);">
            {{ t('createContainer.gpuNoneOnNode') }}
          </div>
          <div v-else style="width:100%;display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:8px;">
            <div
              v-for="d in gpuDevices"
              :key="d.index"
              :class="['gpu-card', {
                selected: form.gpu_indices.includes(d.index),
                disabled: !!d.held_by,
              }]"
              @click="toggleIndex(d.index, !!d.held_by)"
            >
              <div class="gpu-card-row">
                <strong>GPU {{ d.index }}</strong>
                <span v-if="d.held_by" class="pill held">
                  {{ t('createContainer.gpuHeldBy', { name: d.held_by.container_name || d.held_by.container_id }) }}
                </span>
                <span v-else class="pill free">{{ t('createContainer.gpuFreePill') }}</span>
              </div>
              <div v-if="d.name" style="font-size:12px;color:var(--text-dim);margin-top:4px;">{{ d.name }}</div>
              <div v-if="d.mem_total_bytes" style="font-size:11px;color:var(--text-dim);">
                {{ fmtBytes(d.mem_total_bytes) }}
              </div>
            </div>
          </div>
          <div v-if="gpuDevices.length" style="margin-top:8px;font-size:12px;color:var(--text-dim);">
            {{ t('createContainer.gpuSelectedCount', { n: form.gpu_indices.length }) }}
          </div>
        </el-form-item>

        <el-divider>{{ t('createContainer.dividerScheduling') }}</el-divider>

        <el-form-item :label="t('createContainer.fieldTargetNode')">
          <el-select
            v-model="form.node_id"
            :placeholder="t('createContainer.autoSelect')"
            :clearable="form.gpu_mode !== 'pick'"
            style="width:100%;"
          >
            <el-option
              v-for="n in pickerNodes"
              :key="n.id"
              :label="nodeLabel(n)"
              :value="n.id"
            />
          </el-select>
        </el-form-item>

        <div v-if="needsGpu && !gpuCapableNodes.length" style="font-size:12px;color:var(--accent);">
          <el-icon><WarningFilled /></el-icon>
          {{ t('createContainer.gpuNoCapableNodes') }}
        </div>
        <div v-else-if="form.gpu_mode === 'auto' && form.gpu_count > 0 && !form.node_id" style="font-size:12px;color:var(--accent);">
          <el-icon><WarningFilled /></el-icon>
          {{ t('createContainer.gpuPinHint') }}
        </div>

        <el-divider />

        <div style="display:flex;gap:12px;">
          <el-button type="primary" icon="Promotion" :loading="submitting" :disabled="!canSubmit" @click="submit">
            {{ t('createContainer.buttonCreate') }}
          </el-button>
          <el-button @click="reset" :disabled="submitting">{{ t('createContainer.buttonReset') }}</el-button>
          <el-button text @click="router.push('/containers')">{{ t('createContainer.buttonViewAll') }}</el-button>
        </div>
      </el-form>
    </el-card>

    <el-card v-if="result" style="margin-top:16px;">
      <template #header>
        <div style="display:flex;align-items:center;gap:8px;">
          <el-icon color="var(--green)"><CircleCheckFilled /></el-icon>
          <span>{{ t('createContainer.resultTitle') }}</span>
        </div>
      </template>
      <el-descriptions :column="2" border>
        <el-descriptions-item :label="t('createContainer.resultContainerId')">{{ result.container.id }}</el-descriptions-item>
        <el-descriptions-item :label="t('createContainer.resultNode')">{{ result.node.name }}</el-descriptions-item>
        <el-descriptions-item :label="t('createContainer.resultImage')">{{ result.container.image }}</el-descriptions-item>
        <el-descriptions-item :label="t('createContainer.resultState')">
          <span :class="['status-dot', result.container.state]"></span>{{ stateLabel(result.container.state) }}
        </el-descriptions-item>
        <el-descriptions-item v-if="result.gpu_indices && result.gpu_indices.length" :label="t('createContainer.resultGpuIndices')" :span="2">
          {{ result.gpu_indices.join(', ') }}
        </el-descriptions-item>
        <el-descriptions-item v-if="result.external_url" :label="t('createContainer.resultPublicUrl')" :span="2">
          <a :href="result.external_url" target="_blank">{{ result.external_url }}</a>
        </el-descriptions-item>
      </el-descriptions>
      <div style="margin-top:12px;font-size:12px;color:var(--text-dim);">
        {{ t('createContainer.resultHint') }}
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.gpu-card {
  border: 1px solid var(--border, #444);
  border-radius: 6px;
  padding: 10px 12px;
  cursor: pointer;
  transition: border-color 0.15s, background-color 0.15s;
  user-select: none;
}
.gpu-card:hover:not(.disabled) {
  border-color: var(--accent, #66b1ff);
}
.gpu-card.selected {
  border-color: var(--accent, #66b1ff);
  background-color: rgba(102, 177, 255, 0.08);
}
.gpu-card.disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.gpu-card-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.pill {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
}
.pill.free {
  background: rgba(80, 200, 120, 0.18);
  color: #5bc983;
}
.pill.held {
  background: rgba(244, 67, 54, 0.18);
  color: #f4a4a0;
}
</style>
