<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { deleteContainer, fmtBytes, fmtTime, listContainers, listNodes, type Container, type Node } from '@/api/client'

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

async function onDelete(c: Container) {
  try {
    await ElMessageBox.confirm(`Delete container ${c.name}?`, 'Confirm', { type: 'warning' })
  } catch {
    return
  }
  try {
    await deleteContainer(c.id)
    ElMessage.success('Container deleted')
    refresh()
  } catch (e: any) {
    ElMessage.error('Delete failed: ' + (e?.response?.data?.error || e?.message))
  }
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">Containers</h1>
        <div class="page-subtitle">unified view across all nodes · {{ containers.length }} total</div>
      </div>
      <el-button type="primary" @click="$router.push('/containers/new')">
        <el-icon><CirclePlus /></el-icon><span>New container</span>
      </el-button>
    </div>

    <el-card>
      <el-table :data="containers" stripe empty-text="No containers yet">
        <el-table-column prop="name" label="Name" min-width="160" />
        <el-table-column prop="image" label="Image" min-width="200" />
        <el-table-column label="Node" min-width="140">
          <template #default="{ row }">{{ nodeName(row.node_id) }}</template>
        </el-table-column>
        <el-table-column label="State" width="130">
          <template #default="{ row }">
            <span :class="['status-dot', row.state]"></span>{{ row.state }}
          </template>
        </el-table-column>
        <el-table-column label="Status" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">{{ row.status || '—' }}</template>
        </el-table-column>
        <el-table-column label="Resources" min-width="180">
          <template #default="{ row }">
            <span v-if="row.cpu_cores">CPU {{ row.cpu_cores }}c</span>
            <span v-if="row.cpu_cores && row.memory_bytes"> · </span>
            <span v-if="row.memory_bytes">{{ fmtBytes(row.memory_bytes) }}</span>
            <span v-if="row.gpu_count"> · {{ row.gpu_count }}×GPU</span>
            <span v-if="!row.cpu_cores && !row.memory_bytes && !row.gpu_count">unlimited</span>
          </template>
        </el-table-column>
        <el-table-column label="External" min-width="140">
          <template #default="{ row }">
            <a v-if="row.external_url" :href="row.external_url" target="_blank">{{ row.external_url }}</a>
            <span v-else style="color:var(--text-dim);">—</span>
          </template>
        </el-table-column>
        <el-table-column label="Created" min-width="120">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="Actions" width="120">
          <template #default="{ row }">
            <el-button size="small" type="danger" link @click="onDelete(row)">Delete</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>
