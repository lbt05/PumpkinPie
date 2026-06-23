<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteContainer,
  fmtDateTime,
  fmtTime,
  listContainers,
  listNodes,
  resourcesLabel,
  startContainer,
  stateClass,
  stopContainer,
  type Container,
  type Node,
} from '@/api/client'

function shortImageName(image: string): string {
  if (!image) return ''
  const atIdx = image.indexOf('@')
  const base = atIdx >= 0 ? image.substring(0, atIdx) : image
  const lastSlash = base.lastIndexOf('/')
  const namePart = lastSlash >= 0 ? base.substring(lastSlash + 1) : base
  const colon = namePart.lastIndexOf(':')
  return colon >= 0 ? namePart.substring(0, colon) : namePart
}

const { t } = useI18n()
const router = useRouter()

const containers = ref<Container[]>([])
const nodes = ref<Node[]>([])
const lastPoll = ref<string>('—')
const lastError = ref<string>('')
const stateFilter = ref<string>('all')
const query = ref<string>('')
let timer: number | undefined

async function refresh() {
  try {
    const [c, n] = await Promise.all([listContainers(), listNodes()])
    containers.value = c
    nodes.value = n
    lastPoll.value = fmtTime(new Date().toISOString())
    lastError.value = ''
  } catch (e: any) {
    lastError.value = e?.response?.data?.error || e?.message || 'network error'
  }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 4000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

function nodeName(id: string) {
  const n = nodes.value.find((x) => x.id === id)
  return n ? n.name : id
}

const stateChips = computed(() => [
  { v: 'all',      label: t('containers.stateFilterAll') },
  { v: 'running',  label: t('containers.stateFilterRunning') },
  { v: 'starting', label: t('containers.stateFilterStarting') },
  { v: 'stopping', label: t('containers.stateFilterStopping') },
  { v: 'exited',   label: t('containers.stateFilterExited') },
  { v: 'error',    label: t('containers.stateFilterError') },
])

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return containers.value.filter((c) => {
    if (stateFilter.value !== 'all' && c.state !== stateFilter.value) return false
    if (q) {
      if (
        !(c.name || '').toLowerCase().includes(q) &&
        !(c.image || '').toLowerCase().includes(q) &&
        !(c.node_name || nodeName(c.node_id) || '').toLowerCase().includes(q)
      ) return false
    }
    return true
  })
})

function toggleActionFor(state: string): 'start' | 'stop' | 'none' {
  if (state === 'running') return 'stop'
  if (state === 'exited') return 'start'
  // starting, stopping, error all show "no action" — let the
  // agent's ack land first so we never queue a duplicate command.
  return 'none'
}

async function confirm(title: string, msg: string, okLabel: string) {
  try {
    await ElMessageBox.confirm(msg, title, {
      type: 'warning',
      confirmButtonText: okLabel,
      cancelButtonText: t('common.cancel'),
    })
    return true
  } catch { return false }
}

async function onToggle(c: Container) {
  const action = toggleActionFor(c.state)
  if (action === 'none') return
  if (action === 'stop') {
    if (!await confirm(t('containers.stopConfirm', { name: c.name }), `“${c.name}” ${t('confirm.stopMsg')}`, t('act.stop'))) return
    try {
      await stopContainer(c.id)
      ElMessage({ type: 'success', message: t('containers.stopSuccess') })
    } catch (e: any) {
      ElMessage({ type: 'error', message: t('containers.stopFailed', { msg: e?.response?.data?.error || e?.message }) })
      return
    }
  } else {
    try {
      await startContainer(c.id)
      ElMessage({ type: 'success', message: t('containers.startSuccess') })
    } catch (e: any) {
      ElMessage({ type: 'error', message: t('containers.startFailed', { msg: e?.response?.data?.error || e?.message }) })
      return
    }
  }
  refresh()
}

async function onDelete(c: Container) {
  if (!await confirm(t('containers.deleteTitle', { name: c.name }), `“${c.name}” ${t('confirm.deleteMsg')}`, t('act.delete'))) return
  try {
    await deleteContainer(c.id)
    ElMessage({ type: 'success', message: t('containers.deleteSuccess') })
    refresh()
  } catch (e: any) {
    ElMessage({ type: 'error', message: t('containers.deleteFailed', { msg: e?.response?.data?.error || e?.message }) })
  }
}

async function onCopyImage(image: string) {
  if (!image) return
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(image)
    } else {
      const ta = document.createElement('textarea')
      ta.value = image
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    ElMessage({ type: 'success', message: t('containers.copyImageSuccess') })
  } catch {
    ElMessage({ type: 'error', message: t('containers.copyImageFailed') })
  }
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('page.containers.title') }}</h1>
        <p class="page-subtitle">
          <span>{{ t('containers.subtitle') }}</span>
          <span class="dot" />
          <span class="mono">{{ t('containers.shownOfTotal', { shown: filtered.length, total: containers.length }) }}</span>
          <span class="dot" />
          <span class="mono">{{ lastPoll }}</span>
        </p>
      </div>
      <div class="page-actions">
        <button class="btn is-primary" type="button" @click="router.push('/containers/new')">
          <span class="ico" aria-hidden="true">＋</span>
          <span>{{ t('containers.buttonNew') }}</span>
        </button>
      </div>
    </header>

    <!-- Filters -->
    <div class="filters">
      <div class="search">
        <input v-model="query" type="search" :placeholder="t('containers.searchPlaceholder')" aria-label="Search containers" />
      </div>
      <div class="row-tight" role="tablist" :aria-label="t('page.containers.title')">
        <button
          v-for="c in stateChips"
          :key="c.v"
          class="chip"
          :class="{ 'is-active': stateFilter === c.v }"
          :aria-selected="stateFilter === c.v"
          type="button"
          @click="stateFilter = c.v"
        >{{ c.label }}</button>
      </div>
    </div>

    <div v-if="lastError" class="banner" role="alert">
      <span class="ico" aria-hidden="true">⚠</span>
      <span><strong>{{ t('dashboard.reconnecting') }}</strong> {{ lastError }}</span>
    </div>

    <div v-if="!containers.length" class="empty">
      <div class="ico" aria-hidden="true">▢</div>
      <p class="empty-title">{{ t('containers.empty') }}</p>
    </div>
    <div v-else-if="!filtered.length" class="empty">
      <div class="ico" aria-hidden="true">▢</div>
      <p class="empty-title">{{ t('containers.emptyFilter') }}</p>
      <p class="empty-hint">{{ t('containers.emptyFilterHint') }}</p>
    </div>
    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>{{ t('col.name') }}</th>
            <th>{{ t('col.image') }}</th>
            <th>{{ t('col.node') }}</th>
            <th>{{ t('col.state') }}</th>
            <th>{{ t('col.resources') }}</th>
            <th>{{ t('col.external') }}</th>
            <th>{{ t('col.created') }}</th>
            <th class="col-actions">{{ t('col.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in filtered" :key="c.id" :data-state-row="c.state">
            <td>
              <span class="col-name">
                <a v-if="c.external_url" :href="c.external_url" target="_blank" rel="noopener" class="visit-name" :title="c.external_url">{{ c.name }}</a>
                <template v-else>{{ c.name }}</template>
              </span>
            </td>
            <td class="col-image">
              <span class="image-cell">
                <el-tooltip :content="c.image" placement="top" :show-after="100">
                  <span class="image-name mono">{{ shortImageName(c.image) }}</span>
                </el-tooltip>
                <el-tooltip :content="t('containers.copyImage')" placement="top">
                  <button
                    class="btn-icon copy-btn"
                    type="button"
                    :aria-label="t('containers.copyImage')"
                    @click="onCopyImage(c.image)"
                  >
                    <span class="ico" aria-hidden="true">⧉</span>
                  </button>
                </el-tooltip>
              </span>
            </td>
            <td class="col-muted mono">{{ c.node_name || nodeName(c.node_id) }}</td>
            <td>
              <el-tooltip
                :content="c.status || ''"
                placement="top"
                :disabled="!c.status"
                :show-after="100"
              >
                <span class="status-cell">
                  <span class="status-dot" :class="c.state" />
                  <span class="badge" :class="stateClass(c.state)" style="margin-left:6px;">{{ t('state.' + c.state, c.state) }}</span>
                </span>
              </el-tooltip>
            </td>
            <td>
              <span class="resource-tag">
                <template v-for="(s, i) in resourcesLabel(c)" :key="i">
                  <span v-if="i > 0" class="rt-sep">·</span>
                  <span :class="['rt-seg', s.cls]">{{ s.text }}</span>
                </template>
              </span>
            </td>
            <td class="col-muted">
              <el-tooltip
                v-if="c.external_url"
                :content="t('containers.visitExternal')"
                placement="top"
              >
                <a
                  :href="c.external_url"
                  target="_blank"
                  rel="noopener"
                  class="visit-link"
                  :title="c.external_url"
                  :aria-label="t('containers.visitExternal')"
                >
                  <el-icon class="ext-ico"><TopRight /></el-icon>
                </a>
              </el-tooltip>
              <span v-else class="faint">{{ t('common.na') }}</span>
            </td>
            <td class="col-muted mono">{{ fmtDateTime(c.created_at) }}</td>
            <td class="col-actions">
              <div class="row-actions row-tight">
                <button
                  v-if="toggleActionFor(c.state) === 'start'"
                  class="btn is-small is-toggle is-start"
                  type="button"
                  :aria-label="t('act.start') + ' ' + c.name"
                  @click="onToggle(c)"
                >
                  <span class="ico" aria-hidden="true">▶</span>
                  <span>{{ t('containers.btnStart') }}</span>
                </button>
                <button
                  v-else-if="toggleActionFor(c.state) === 'stop'"
                  class="btn is-small is-toggle is-stop"
                  type="button"
                  :aria-label="t('act.stop') + ' ' + c.name"
                  @click="onToggle(c)"
                >
                  <span class="ico" aria-hidden="true">■</span>
                  <span>{{ t('containers.btnStop') }}</span>
                </button>
                <span v-else class="muted" style="font-size: var(--fs-sm);">{{ t('containers.noAction') }}</span>
                <button
                  class="btn is-small is-danger"
                  type="button"
                  :aria-label="t('act.delete') + ' ' + c.name"
                  @click="onDelete(c)"
                >
                  <span class="ico" aria-hidden="true">✕</span>
                  <span>{{ t('containers.btnDelete') }}</span>
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="faint mono" style="text-align:center; margin-top: var(--sp-9); font-size: var(--fs-xs);">
      {{ t('containers.pollFooter') }}
    </p>
  </div>
</template>

<style scoped>
td.col-actions { white-space: nowrap; }
td.col-actions .row-actions { justify-content: flex-end; gap: 6px; }

.col-name a {
  color: var(--text);
  border-bottom: 1px dashed color-mix(in oklab, var(--text-dim) 60%, transparent);
  transition: color var(--t-fast), border-color var(--t-fast);
}
.col-name a:hover {
  color: var(--accent);
  border-bottom-color: var(--accent);
  text-decoration: none;
}
.col-name a .ext-ico { color: var(--text-faint); font-size: 11px; margin-left: 4px; vertical-align: middle; }
.col-name a:hover .ext-ico { color: var(--accent); }

/* Image cell — short name + copy button. */
.col-image { max-width: 280px; }
.image-cell {
  display: inline-flex; align-items: center; gap: 6px;
  max-width: 100%;
}
.image-name {
  max-width: 220px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  display: inline-block;
  vertical-align: middle;
  cursor: default;
}
.copy-btn {
  flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px;
  padding: 0;
  border-radius: var(--r-2);
  border: 1px solid var(--border-soft);
  background: transparent;
  color: var(--text-faint);
  cursor: pointer;
  transition: color var(--t-fast), background var(--t-fast), border-color var(--t-fast);
}
.copy-btn:hover {
  color: var(--accent);
  background: var(--bg-elev);
  border-color: color-mix(in oklab, var(--accent) 35%, transparent);
}
.copy-btn:active { transform: translateY(1px); }
.copy-btn .ico { font-size: 13px; line-height: 1; }

/* Status cell — keeps dot + badge inline so the tooltip wraps the whole row. */
.status-cell { display: inline-flex; align-items: center; cursor: default; }

/* External link — clear, accent-tinted icon button so it reads as "click me". */
.visit-link {
  display: inline-flex; align-items: center; justify-content: center;
  width: 34px; height: 34px;
  border-radius: var(--r-3);
  border: 1px solid color-mix(in oklab, var(--accent) 35%, transparent);
  background: color-mix(in oklab, var(--accent) 12%, transparent);
  color: var(--accent);
  transition: color var(--t-fast), background var(--t-fast), border-color var(--t-fast),
              transform var(--t-fast), box-shadow var(--t-fast);
  text-decoration: none;
}
.visit-link:hover {
  color: var(--accent-hover);
  background: color-mix(in oklab, var(--accent) 20%, transparent);
  border-color: color-mix(in oklab, var(--accent) 55%, transparent);
  transform: translateY(-1px);
  box-shadow: 0 2px 8px color-mix(in oklab, var(--accent) 18%, transparent);
  text-decoration: none;
}
.visit-link:active { transform: translateY(0); }
.visit-link:focus-visible {
  outline: 2px solid color-mix(in oklab, var(--accent) 60%, transparent);
  outline-offset: 2px;
}
.visit-link .ext-ico { font-size: 18px; line-height: 1; }

/* Toggle button — same shape, label switches by state. */
.btn.is-toggle { min-width: 64px; justify-content: center; }
.btn.is-toggle.is-stop { color: var(--warn); }
.btn.is-toggle.is-stop:hover { background: var(--warn-soft); }
.btn.is-toggle.is-start {
  color: var(--success);
  background: color-mix(in oklab, var(--success) 12%, transparent);
  border-color: color-mix(in oklab, var(--success) 35%, transparent);
}
.btn.is-toggle.is-start:hover { background: color-mix(in oklab, var(--success) 20%, transparent); }
</style>
