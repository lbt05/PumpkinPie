<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteContainer, fmtBytes, fmtTime, listContainers, listNodes,
  startContainer, stopContainer,
  type Container, type Node,
} from '@/api/client'

const { t } = useI18n()
const containers = ref<Container[]>([])
const nodes = ref<Node[]>([])
let timer: number | undefined

async function refresh() {
  try {
    [containers.value, nodes.value] = await Promise.all([listContainers(), listNodes()])
  } catch (e) {
    console.warn(e)
  }
}
onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 4000)
})
onUnmounted(() => timer && clearInterval(timer))

function nodeName(id: string) {
  const n = nodes.value.find((n) => n.id === id)
  return n ? n.name : id
}

function stateLabel(s: string) {
  return t(`state.${s}` as any, s)
}

// Map container state to the single toggle action to show.
//   running, created  -> action = stop (red warning button)
//   exited, stopped   -> action = start (primary button)
//   error, creating   -> no action (busy / error)
type ToggleAction = 'start' | 'stop' | 'none'
function toggleActionFor(state: string): ToggleAction {
  switch (state) {
    case 'running':
    case 'created':
      return 'stop'
    case 'exited':
    case 'stopped':
      return 'start'
    default:
      return 'none'
  }
}

function resourcesLabel(c: Container): string {
  const hasCpu = c.cpu_cores > 0
  const hasMem = c.memory_bytes > 0
  const hasGpu = c.gpu_count > 0
  if (!hasCpu && !hasMem && !hasGpu) return t('common.unlimited')
  const mem = hasMem ? fmtBytes(c.memory_bytes) : ''
  let base: string
  if (hasCpu && hasMem && hasGpu) {
    base = t('resources.cpuMemGpu', { cpu: c.cpu_cores, mem, n: c.gpu_count })
  } else if (hasCpu && hasMem) {
    base = t('resources.cpuAndMem', { cpu: c.cpu_cores, mem })
  } else if (hasCpu && hasGpu) {
    base = t('resources.cpuOnly', { n: c.cpu_cores }) + ' · ' + t('resources.gpuOnly', { n: c.gpu_count })
  } else if (hasMem && hasGpu) {
    base = mem + ' · ' + t('resources.gpuOnly', { n: c.gpu_count })
  } else if (hasCpu) {
    base = t('resources.cpuOnly', { n: c.cpu_cores })
  } else if (hasMem) {
    base = mem
  } else {
    base = t('resources.gpuOnly', { n: c.gpu_count })
  }
  if (hasGpu && c.gpu_indices && c.gpu_indices.length > 0) {
    base += ' · ' + t('resources.gpuIndices', { idx: c.gpu_indices.join(', ') })
  }
  return base
}

async function confirm(title: string): Promise<boolean> {
  try {
    await ElMessageBox.confirm(title, t('common.confirm'), { type: 'warning' })
    return true
  } catch {
    return false
  }
}

async function onToggle(c: Container) {
  const action = toggleActionFor(c.state)
  if (action === 'none') return
  if (action === 'stop') {
    if (!(await confirm(t('containers.stopConfirm', { name: c.name })))) return
    try {
      await stopContainer(c.id)
      ElMessage.success(t('containers.stopSuccess'))
    } catch (e: any) {
      ElMessage.error(t('containers.stopFailed', { msg: e?.response?.data?.error || e?.message }))
      return
    }
  } else {
    try {
      await startContainer(c.id)
      ElMessage.success(t('containers.startSuccess'))
    } catch (e: any) {
      ElMessage.error(t('containers.startFailed', { msg: e?.response?.data?.error || e?.message }))
      return
    }
  }
  // Optimistic UI; the auto-refresh will reconcile shortly.
  refresh()
}

async function onDelete(c: Container) {
  if (!(await confirm(t('containers.deleteTitle', { name: c.name })))) return
  try {
    await deleteContainer(c.id)
    ElMessage.success(t('containers.deleteSuccess'))
    refresh()
  } catch (e: any) {
    ElMessage.error(t('containers.deleteFailed', { msg: e?.response?.data?.error || e?.message }))
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('containers.title') }}</h1>
        <div class="page-subtitle">{{ t('containers.subtitle', { n: containers.length }) }}</div>
      </div>
      <el-button type="primary" @click="$router.push('/containers/new')">
        <el-icon><CirclePlus /></el-icon><span>{{ t('containers.buttonNew') }}</span>
      </el-button>
    </div>

    <el-card>
      <el-table :data="containers" stripe :empty-text="t('containers.empty')">
        <el-table-column :label="t('containers.colName')" prop="name" min-width="160" />
        <el-table-column :label="t('containers.colImage')" prop="image" min-width="200" />
        <el-table-column :label="t('containers.colNode')" min-width="140">
          <template #default="{ row }">{{ nodeName(row.node_id) }}</template>
        </el-table-column>
        <el-table-column :label="t('containers.colState')" width="120">
          <template #default="{ row }">
            <span :class="['status-dot', row.state]"></span>{{ stateLabel(row.state) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('containers.colStatus')" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">{{ row.status || t('common.na') }}</template>
        </el-table-column>
        <el-table-column :label="t('containers.colResources')" min-width="180">
          <template #default="{ row }">{{ resourcesLabel(row) }}</template>
        </el-table-column>
        <el-table-column :label="t('containers.colExternal')" min-width="140">
          <template #default="{ row }">
            <a v-if="row.external_url" :href="row.external_url" target="_blank">{{ row.external_url }}</a>
            <span v-else style="color:var(--text-dim);">{{ t('common.na') }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('containers.colCreated')" min-width="120">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('containers.colActions')" width="180" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="toggleActionFor(row.state) === 'start'"
              size="small"
              type="primary"
              @click="onToggle(row)"
            >{{ t('containers.btnStart') }}</el-button>
            <el-button
              v-else-if="toggleActionFor(row.state) === 'stop'"
              size="small"
              type="warning"
              @click="onToggle(row)"
            >{{ t('containers.btnStop') }}</el-button>
            <span v-else style="color:var(--text-dim);font-size:12px;">{{ t('containers.noAction') }}</span>
            <el-button
              size="small"
              type="danger"
              link
              style="margin-left:8px;"
              @click="onDelete(row)"
            >{{ t('containers.btnDelete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>
