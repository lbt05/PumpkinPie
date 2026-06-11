<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  listContainers,
  listNodes,
  fmtBytes,
  fmtDateTime,
  fmtTime,
  stateClass,
  type Container,
  type Node,
} from '@/api/client'

const { t } = useI18n()
const router = useRouter()

const nodes = ref<Node[]>([])
const containers = ref<Container[]>([])
const lastPoll = ref<string>('—')
const lastError = ref<string>('')
const lastErrorAt = ref<number>(0)
let timer: number | undefined

async function refresh() {
  try {
    const [n, c] = await Promise.all([listNodes(), listContainers()])
    nodes.value = n
    containers.value = c
    lastPoll.value = fmtTime(new Date().toISOString())
    lastError.value = ''
  } catch (e: any) {
    lastError.value = e?.response?.data?.error || e?.message || 'network error'
    lastErrorAt.value = Date.now()
  }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

const online = computed(() => nodes.value.filter((n) => n.state === 'online'))
const offline = computed(() => nodes.value.filter((n) => n.state !== 'online'))

const totalCores = computed(() => online.value.reduce((s, n) => s + (n.cpu_cores || 0), 0))
const totalMem = computed(() => online.value.reduce((s, n) => s + (n.mem_total_bytes || 0), 0))
const usedMem = computed(() => online.value.reduce((s, n) => s + (n.mem_used_bytes || 0), 0))
const totalGpu = computed(() => online.value.reduce((s, n) => s + (n.gpu_count || 0), 0))

const running = computed(() => containers.value.filter((c) => c.state === 'running').length)
const starting = computed(() => containers.value.filter((c) => c.state === 'starting').length)
const stopping = computed(() => containers.value.filter((c) => c.state === 'stopping').length)
const errored = computed(() => containers.value.filter((c) => c.state === 'error').length)

const avgCpu = computed(() => {
  if (!online.value.length) return 0
  return Math.round(online.value.reduce((s, n) => s + (n.cpu_percent || 0), 0) / online.value.length)
})
const memPct = computed(() => totalMem.value ? Math.round((usedMem.value / totalMem.value) * 100) : 0)
const avgGpu = computed(() => {
  const gpus = online.value.filter((n) => (n.gpu_count || 0) > 0)
  if (!gpus.length) return 0
  return Math.round(gpus.reduce((s, n) => s + (n.gpu_usage_percent || 0), 0) / gpus.length)
})
const hasGpu = computed(() => totalGpu.value > 0)

const errorAgo = computed(() => {
  if (!lastErrorAt.value) return '0 s'
  return fmtAge(new Date(lastErrorAt.value).toISOString())
})

const recent = computed(() =>
  containers.value.slice().sort((a, b) => new Date(b.updated_at || b.created_at).getTime() - new Date(a.updated_at || a.created_at).getTime()).slice(0, 8)
)

function fmtAge(iso: string): string {
  const d = new Date(iso).getTime()
  const ms = Date.now() - d
  if (ms < 60_000) return Math.max(0, Math.floor(ms / 1000)) + ' s'
  return Math.floor(ms / 60_000) + ' m'
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('page.dashboard.title') }}</h1>
        <p class="page-subtitle">
          <span>{{ t('page.dashboard.subtitle') }}</span>
          <span class="dot" />
          <span>{{ t('common.lastPoll') }}</span>
          <span class="mono" style="color: var(--text);">{{ lastPoll }}</span>
        </p>
      </div>
      <div class="page-actions">
        <button class="btn is-primary" type="button" @click="router.push('/containers/new')">
          <span class="ico" aria-hidden="true">＋</span>
          <span>{{ t('page.dashboard.cta') }}</span>
        </button>
      </div>
    </header>

    <!-- Poll-error banner -->
    <div v-if="lastError" class="banner" role="alert">
      <span class="ico" aria-hidden="true">⚠</span>
      <span>
        <strong>{{ t('dashboard.reconnecting') }}</strong>
        {{ t('dashboard.reconnectingDesc', { ago: errorAgo, code: lastError }) }}
      </span>
    </div>

    <!-- ====== NODES section ====== -->
    <section class="section">
      <div class="dash-kpis" style="margin-bottom: var(--sp-2);">
        <span class="kpi-domain">{{ t('dashboard.kpiNodes') }}</span>
        <span class="kpi is-online">online <strong>{{ online.length }}</strong></span>
        <span class="sep" />
        <span class="kpi is-offline">offline <strong>{{ offline.length }}</strong></span>
        <span class="sep" />
        <span class="kpi">{{ t('common.cores') }} <strong>{{ totalCores }}</strong></span>
        <span class="sep" />
        <span class="kpi">mem <strong>{{ fmtBytes(usedMem) }} / {{ fmtBytes(totalMem) }}</strong></span>
        <span class="sep" />
        <span class="kpi">GPUs <strong>{{ totalGpu }}</strong></span>
      </div>

      <div v-if="online.length" class="dash-kpis is-metrics" style="margin-bottom: var(--sp-2);">
        <span class="kpi-domain">{{ t('dashboard.avgLabel') }}</span>
        <span class="kpi is-cpu">CPU <strong>{{ avgCpu }}%</strong></span>
        <span class="sep" />
        <span class="kpi is-mem">MEM <strong>{{ memPct }}%</strong></span>
        <span v-if="hasGpu" class="kpi is-gpu" style="margin-left: var(--sp-3);">GPU <strong>{{ avgGpu }}%</strong></span>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">{{ t('dashboard.pernodeTitle') }}</h3>
          <RouterLink class="card-sub" to="/nodes" style="text-decoration: none;">
            <span>{{ t('dashboard.pernodeCta') }}</span>
          </RouterLink>
        </div>
        <div v-if="online.length" class="chart">
          <div v-for="n in online" :key="n.id" class="chart-row">
            <div class="name">
              <span class="status-dot online" aria-hidden="true" />
              {{ n.name }}
            </div>
            <div class="bars">
              <div class="bar-mini cpu">
                <span class="lbl">CPU</span>
                <div class="bar"><div class="fill" :style="{ width: (n.cpu_percent || 0) + '%' }" /></div>
                <span class="lbl" style="text-align:right;">{{ Math.round(n.cpu_percent || 0) }}%</span>
              </div>
              <div class="bar-mini mem">
                <span class="lbl">MEM</span>
                <div class="bar"><div class="fill" :style="{ width: (n.mem_total_bytes ? Math.round((n.mem_used_bytes / n.mem_total_bytes) * 100) : 0) + '%' }" /></div>
                <span class="lbl" style="text-align:right;">{{ n.mem_total_bytes ? Math.round((n.mem_used_bytes / n.mem_total_bytes) * 100) : 0 }}%</span>
              </div>
              <div v-if="(n.gpu_count || 0) > 0" class="bar-mini gpu">
                <span class="lbl">GPU</span>
                <div class="bar"><div class="fill" :style="{ width: (n.gpu_usage_percent || 0) + '%' }" /></div>
                <span class="lbl" style="text-align:right;">{{ Math.round(n.gpu_usage_percent || 0) }}%</span>
              </div>
            </div>
          </div>
        </div>
        <div v-else class="empty">
          <div class="ico" aria-hidden="true">▣</div>
          <p class="empty-title">{{ t('dashboard.emptyNodes') }}</p>
          <p class="empty-hint">
            {{ t('dashboard.emptyNodesHint') }}
            <code class="code">pp agent --master=master:7000</code>
          </p>
        </div>
      </div>
    </section>

    <!-- ====== CONTAINERS section ====== -->
    <section class="section">
      <div class="dash-kpis" style="margin-bottom: var(--sp-2);">
        <span class="kpi-domain">{{ t('dashboard.kpiContainers') }}</span>
        <span class="kpi is-running">running <strong>{{ running }}</strong></span>
        <span class="sep" />
        <span class="kpi is-creating">starting <strong>{{ starting }}</strong></span>
        <span class="sep" />
        <span class="kpi is-stopping">stopping <strong>{{ stopping }}</strong></span>
        <span class="sep" />
        <span class="kpi is-error">errors <strong>{{ errored }}</strong></span>
        <span class="sep" />
        <span class="kpi">total <strong>{{ containers.length }}</strong></span>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">{{ t('dashboard.recentTitle') }}</h3>
          <RouterLink class="card-sub" to="/containers" style="text-decoration: none;">
            <span>{{ t('dashboard.recentCta') }}</span>
          </RouterLink>
        </div>
        <div v-if="recent.length" class="table-wrap">
          <table class="table is-fixed">
            <colgroup>
              <col style="width: 22%;">
              <col style="width: 26%;">
              <col style="width: 12%;">
              <col style="width: 16%;">
              <col style="width: 14%;">
              <col style="width: 10%;">
            </colgroup>
            <thead>
              <tr>
                <th>{{ t('col.name') }}</th>
                <th>{{ t('col.image') }}</th>
                <th>{{ t('col.node') }}</th>
                <th>{{ t('col.state') }}</th>
                <th>{{ t('col.external') }}</th>
                <th>{{ t('col.created') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in recent" :key="c.id">
                <td><span class="col-name col-ellipsis" :title="c.name">{{ c.name }}</span></td>
                <td class="col-muted mono col-ellipsis" :title="c.image">{{ c.image }}</td>
                <td class="col-muted mono col-ellipsis">{{ c.node_name || c.node_id }}</td>
                <td>
                  <span class="status-dot" :class="c.state" />
                  <span class="badge" :class="stateClass(c.state)" style="margin-left:6px;">{{ t('state.' + c.state, c.state) }}</span>
                </td>
                <td class="col-muted col-ellipsis">
                  <a v-if="c.external_url" :href="c.external_url" target="_blank" rel="noopener" class="col-ellipsis">{{ c.external_url.replace(/^https?:\/\//, '') }}</a>
                  <span v-else class="faint">{{ t('common.na') }}</span>
                </td>
                <td class="col-muted mono col-ellipsis">{{ fmtDateTime(c.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">
          <div class="ico" aria-hidden="true">▢</div>
          <p class="empty-title">{{ t('containers.empty') }}</p>
        </div>
      </div>
    </section>
  </div>
</template>
