<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { createContainer, listNodes, type CreateContainerPayload, type Node, type PortMapping } from '@/api/client'

const router = useRouter()
const { t } = useI18n()
const nodes = ref<Node[]>([])

const form = ref({
  image: 'nginx:alpine',
  name: '',
  cmd: '',
  envText: '',
  ports: [{ container_port: 80, protocol: 'tcp' }] as PortMapping[],
  cpu_cores: 0,
  memory_mb: 0,
  gpu_count: 0,
  node_id: '' as string,
  pull: true,
  volume_binds: '',
})

const submitting = ref(false)
const result = ref<{ container: any; external_url?: string; node: { name: string } } | null>(null)

onMounted(async () => {
  try {
    nodes.value = await listNodes()
  } catch (e) {
    console.warn(e)
  }
})

const onlineNodes = computed(() => nodes.value.filter((n) => n.state === 'online'))
const canSubmit = computed(() => form.value.image && onlineNodes.value.length > 0)

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
          <el-col :span="6">
            <el-form-item :label="t('createContainer.fieldCpu')">
              <el-input-number v-model="form.cpu_cores" :min="0" :step="0.5" :precision="1" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item :label="t('createContainer.fieldMemory')">
              <el-input-number v-model="form.memory_mb" :min="0" :step="64" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item :label="t('createContainer.fieldGpu')">
              <el-input-number v-model="form.gpu_count" :min="0" :max="8" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item :label="t('createContainer.fieldPull')">
              <el-switch v-model="form.pull" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-divider>{{ t('createContainer.dividerScheduling') }}</el-divider>

        <el-form-item :label="t('createContainer.fieldTargetNode')">
          <el-select v-model="form.node_id" :placeholder="t('createContainer.autoSelect')" clearable style="width:100%;">
            <el-option
              v-for="n in onlineNodes"
              :key="n.id"
              :label="t('createContainer.nodeOption', { name: n.name, host: n.hostname, cpu: n.cpu_percent.toFixed(0) })"
              :value="n.id"
            />
          </el-select>
        </el-form-item>

        <div v-if="form.gpu_count > 0 && !form.node_id" style="font-size:12px;color:var(--accent);">
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
