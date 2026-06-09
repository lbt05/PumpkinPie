<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { listNodes, type Node } from '@/api/client'

const route = useRoute()
const onlineCount = ref(0)
const totalCount = ref(0)
let timer: number | undefined

async function refresh() {
  try {
    const nodes: Node[] = await listNodes()
    totalCount.value = nodes.length
    onlineCount.value = nodes.filter((n) => n.state === 'online').length
  } catch {}
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

const items = computed(() => [
  { name: 'Dashboard', icon: 'DataLine', to: '/' },
  { name: 'Nodes', icon: 'Cpu', to: '/nodes' },
  { name: 'Containers', icon: 'Box', to: '/containers' },
  { name: 'New Container', icon: 'CirclePlus', to: '/containers/new' },
])
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-brand">
      <span>🎃</span>
      <span>pumpkinPie</span>
    </div>
    <RouterLink
      v-for="i in items"
      :key="i.to"
      :to="i.to"
      class="nav-item"
      :class="{ active: route.path === i.to }"
    >
      <el-icon><component :is="i.icon" /></el-icon>
      <span>{{ i.name }}</span>
    </RouterLink>
    <div style="flex:1"></div>
    <div style="font-size:12px;color:var(--text-dim);padding:8px 14px;">
      <span class="status-dot" :class="onlineCount>0 ? 'online':'offline'"></span>
      {{ onlineCount }} / {{ totalCount }} nodes online
    </div>
  </aside>
</template>
