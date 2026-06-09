<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { listNodes, type Node } from '@/api/client'
import { setLang, SUPPORTED_LANGS, currentLang, type Lang } from '@/i18n'

const route = useRoute()
const { t, locale } = useI18n()
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
  { name: t('nav.dashboard'), icon: 'DataLine', to: '/' },
  { name: t('nav.nodes'), icon: 'Cpu', to: '/nodes' },
  { name: t('nav.containers'), icon: 'Box', to: '/containers' },
  { name: t('nav.newContainer'), icon: 'CirclePlus', to: '/containers/new' },
])

const lang = ref<Lang>(currentLang())
function onLangChange(v: Lang) {
  setLang(v)
  lang.value = v
}
// keep ref in sync if locale changes elsewhere
watch(locale, (l) => { lang.value = l as Lang })
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-brand">
      <span>🎃</span>
      <span>{{ t('app.brand') }}</span>
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

    <div class="sidebar-lang">
      <el-select
        :model-value="lang"
        @update:model-value="onLangChange"
        size="small"
        style="width:100%;"
      >
        <el-option
          v-for="l in SUPPORTED_LANGS"
          :key="l.value"
          :label="l.label"
          :value="l.value"
        />
      </el-select>
    </div>

    <div class="sidebar-status">
      <span class="status-dot" :class="onlineCount>0 ? 'online':'offline'"></span>
      {{ t('nav.onlineCount', { online: onlineCount, total: totalCount }) }}
    </div>
  </aside>
</template>

<style scoped>
.sidebar-lang {
  padding: 0 4px 8px;
}
.sidebar-status {
  font-size: 12px;
  color: var(--text-dim);
  padding: 8px 14px;
}
</style>
