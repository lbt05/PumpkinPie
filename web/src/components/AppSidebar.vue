<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { listNodes, listContainers, type Node, type Container } from '@/api/client'
import { setLang, theme, SUPPORTED_LANGS, currentLang, type Lang } from '@/i18n'

const route = useRoute()
const { t, locale } = useI18n()

const nodeCount = ref(0)
const ctrCount = ref(0)
const onlineCount = ref(0)
const totalCount = ref(0)
let timer: number | undefined

async function refresh() {
  try {
    const [nodes, ctrs] = await Promise.all([listNodes(), listContainers()])
    nodeCount.value = nodes.length
    ctrCount.value = ctrs.length
    totalCount.value = nodes.length
    onlineCount.value = nodes.filter((n) => n.state === 'online').length
  } catch {}
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

const items = computed(() => [
  {
    name: t('nav.dashboard'),
    to: '/',
    badge: '',
    svg: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="3" width="8" height="5" rx="1.5"/><rect x="13" y="10" width="8" height="11" rx="1.5"/><rect x="3" y="13" width="8" height="8" rx="1.5"/></svg>',
  },
  {
    name: t('nav.nodes'),
    to: '/nodes',
    badge: String(nodeCount.value),
    svg: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="7" rx="1.5"/><rect x="3" y="14" width="18" height="7" rx="1.5"/><circle cx="6.8" cy="6.5" r="1.1" fill="currentColor" stroke="none"/><circle cx="6.8" cy="17.5" r="1.1" fill="currentColor" stroke="none"/><line x1="10.5" y1="6.5" x2="18" y2="6.5"/><line x1="10.5" y1="17.5" x2="18" y2="17.5"/></svg>',
  },
  {
    name: t('nav.containers'),
    to: '/containers',
    badge: String(ctrCount.value),
    svg: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><rect x="2.5" y="6" width="19" height="12" rx="1.5"/><line x1="7" y1="6" x2="7" y2="18"/><line x1="12" y1="6" x2="12" y2="18"/><line x1="17" y1="6" x2="17" y2="18"/></svg>',
  },
  {
    name: t('nav.newContainer'),
    to: '/containers/new',
    badge: '',
    svg: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="9" width="13" height="11" rx="1.5"/><line x1="6.5" y1="9" x2="6.5" y2="20"/><line x1="12.5" y1="9" x2="12.5" y2="20"/><line x1="18" y1="3.5" x2="18" y2="10"/><line x1="14.75" y1="6.75" x2="21.25" y2="6.75"/></svg>',
  },
])

const lang = ref<Lang>(currentLang())
function onLangChange(v: Lang) {
  setLang(v)
  lang.value = v
}
watch(locale, (l) => { lang.value = l as Lang })

const themeLabel = computed(() => theme.current)
function onThemeToggle() {
  theme.toggle()
}
</script>

<template>
  <aside class="sidebar" aria-label="Primary">
    <RouterLink class="sidebar-brand" to="/" aria-label="pumpkinPie home">
      <span class="mark" aria-hidden="true">🎃</span>
      <span class="word">pumpkinPie</span>
    </RouterLink>

    <nav>
      <ul class="nav">
        <li v-for="i in items" :key="i.to">
          <RouterLink
            :to="i.to"
            class="nav-item"
            :class="{ 'is-active': route.path === i.to }"
          >
            <span class="ico" aria-hidden="true" v-html="i.svg" />
            <span>{{ i.name }}</span>
            <span v-if="i.badge" class="badge">{{ i.badge }}</span>
          </RouterLink>
        </li>
      </ul>
    </nav>

    <div class="sidebar-spacer" />

    <div class="sidebar-footer">
      <label class="sidebar-control" :for="'lang-select-' + lang">
        <span class="row-tight">
          <span class="ico" aria-hidden="true">◐</span>
          <span>{{ t('common.language') }}</span>
        </span>
        <select :value="lang" @change="onLangChange(($event.target as HTMLSelectElement).value as Lang)" aria-label="Language">
          <option v-for="l in SUPPORTED_LANGS" :key="l.value" :value="l.value">{{ l.label }}</option>
        </select>
      </label>
      <button class="sidebar-control" type="button" :aria-pressed="theme.current === 'light'" aria-label="Toggle theme" @click="onThemeToggle">
        <span class="row-tight">
          <span class="ico" aria-hidden="true">☾</span>
          <span>{{ t('common.theme') }}</span>
        </span>
        <span class="mono" style="color: var(--text); font-size: var(--fs-xs);">{{ themeLabel }}</span>
      </button>
      <div class="sidebar-status">
        <span class="status-dot" :class="onlineCount === totalCount && totalCount > 0 ? 'online' : (onlineCount === 0 ? 'offline' : 'creating')" aria-hidden="true" />
        <span class="num">{{ t('nav.onlineCount', { online: onlineCount, total: totalCount }) }}</span>
      </div>
    </div>
  </aside>
</template>
