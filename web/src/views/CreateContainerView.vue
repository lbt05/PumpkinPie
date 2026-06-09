<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { createContainer, listNodes, type CreateContainerPayload, type Node, type PortMapping } from '@/api/client'

const router = useRouter()
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
  node_id: '' as string, // empty = auto-pick
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
    ElMessage.success(`Container created on ${r.data.node.name}`)
  } catch (e: any) {
    const msg = e?.response?.data?.error || e?.message || 'unknown error'
    ElMessage.error('Create failed: ' + msg)
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
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">New Container</h1>
        <div class="page-subtitle">
          pick resources; an idle node will be auto-selected if not pinned
        </div>
      </div>
    </div>

    <div v-if="!onlineNodes.length" style="text-align:center;color:var(--text-dim);padding:48px 0;">
      <el-icon :size="48" color="var(--red)"><WarningFilled /></el-icon>
      <div style="margin-top:12px;">No online nodes available.</div>
      <div style="margin-top:8px;font-size:12px;">Start an agent first.</div>
    </div>

    <el-card v-else>
      <el-form label-position="top" :model="form" v-loading="submitting">
        <el-row :gutter="20">
          <el-col :span="14">
            <el-form-item label="Image" required>
              <el-input v-model="form.image" placeholder="e.g. nginx:alpine, redis:7, myregistry.io/app:1.0" />
            </el-form-item>
          </el-col>
          <el-col :span="10">
            <el-form-item label="Name (optional)">
              <el-input v-model="form.name" placeholder="auto-generated if empty" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-form-item label="Command (optional)">
          <el-input v-model="form.cmd" placeholder="e.g. nginx -g 'daemon off;'" />
        </el-form-item>

        <el-form-item label="Environment variables (one KEY=VALUE per line)">
          <el-input
            v-model="form.envText"
            type="textarea"
            :rows="3"
            placeholder="FOO=bar&#10;DEBUG=1"
          />
        </el-form-item>

        <el-form-item label="Volume binds (one host:container[:ro] per line)">
          <el-input
            v-model="form.volume_binds"
            type="textarea"
            :rows="2"
            placeholder="/host/data:/data&#10;/host/config:/etc/app:ro"
          />
        </el-form-item>

        <el-form-item label="Port mappings">
          <div style="width:100%;">
            <div v-for="(p, i) in form.ports" :key="i" style="display:flex;gap:8px;margin-bottom:6px;">
              <el-input-number v-model="p.container_port" :min="1" :max="65535" placeholder="container port" />
              <el-select v-model="p.protocol" style="width:120px;">
                <el-option label="tcp" value="tcp" />
                <el-option label="udp" value="udp" />
              </el-select>
              <el-button icon="Delete" circle @click="removePort(i)" :disabled="form.ports.length<=1" />
            </div>
            <el-button icon="CirclePlus" size="small" @click="addPort">Add port</el-button>
            <span style="margin-left:12px;font-size:12px;color:var(--text-dim);">
              first port gets a public external URL on the master proxy
            </span>
          </div>
        </el-form-item>

        <el-divider>Resources</el-divider>

        <el-row :gutter="20">
          <el-col :span="6">
            <el-form-item label="CPU cores (0 = unlimited)">
              <el-input-number v-model="form.cpu_cores" :min="0" :step="0.5" :precision="1" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item label="Memory (MB, 0 = unlimited)">
              <el-input-number v-model="form.memory_mb" :min="0" :step="64" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item label="GPU count (0 = no GPU)">
              <el-input-number v-model="form.gpu_count" :min="0" :max="8" style="width:100%;" />
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item label="Pull image">
              <el-switch v-model="form.pull" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-divider>Scheduling</el-divider>

        <el-form-item label="Target node (empty = auto-pick least-loaded)">
          <el-select v-model="form.node_id" placeholder="Auto-select" clearable style="width:100%;">
            <el-option
              v-for="n in onlineNodes"
              :key="n.id"
              :label="`${n.name} · ${n.hostname} · CPU ${n.cpu_percent.toFixed(0)}%`"
              :value="n.id"
            />
          </el-select>
        </el-form-item>

        <div v-if="form.gpu_count > 0 && !form.node_id" style="font-size:12px;color:var(--accent);">
          <el-icon><WarningFilled /></el-icon>
          When requesting GPU, consider pinning to a specific node.
        </div>

        <el-divider />

        <div style="display:flex;gap:12px;">
          <el-button type="primary" icon="Promotion" :loading="submitting" :disabled="!canSubmit" @click="submit">
            Create
          </el-button>
          <el-button @click="reset" :disabled="submitting">Reset</el-button>
          <el-button text @click="router.push('/containers')">View all containers</el-button>
        </div>
      </el-form>
    </el-card>

    <el-card v-if="result" style="margin-top:16px;">
      <template #header>
        <div style="display:flex;align-items:center;gap:8px;">
          <el-icon color="var(--green)"><CircleCheckFilled /></el-icon>
          <span>Container created</span>
        </div>
      </template>
      <el-descriptions :column="2" border>
        <el-descriptions-item label="Container ID">{{ result.container.id }}</el-descriptions-item>
        <el-descriptions-item label="Node">{{ result.node.name }}</el-descriptions-item>
        <el-descriptions-item label="Image">{{ result.container.image }}</el-descriptions-item>
        <el-descriptions-item label="State">
          <span :class="['status-dot', result.container.state]"></span>{{ result.container.state }}
        </el-descriptions-item>
        <el-descriptions-item v-if="result.external_url" label="Public URL" :span="2">
          <a :href="result.external_url" target="_blank">{{ result.external_url }}</a>
        </el-descriptions-item>
      </el-descriptions>
      <div style="margin-top:12px;font-size:12px;color:var(--text-dim);">
        Status is being updated asynchronously. Refresh the Containers page in a few seconds.
      </div>
    </el-card>
  </div>
</template>
