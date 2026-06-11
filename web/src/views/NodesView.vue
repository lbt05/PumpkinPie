<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  fmtAge,
  fmtBytes,
  fmtTime,
  fmtUptime,
  listContainers,
  listNodeGPUs,
  listNodes,
  stateClass,
  type Container,
  type Node,
  type NodeGPUDevice,
} from '@/api/client'

const { t } = useI18n()
const router = useRouter()

const nodes = ref<Node[]>([])
const gpuAllocs = ref<Record<string, NodeGPUDevice[]>>({})
const containers = ref<Container[]>([])
const lastPoll = ref<string>('—')
let timer: number | undefined

async function refresh() {
  try {
    const [n, c] = await Promise.all([listNodes(), listContainers()])
    nodes.value = n
    containers.value = c
    const next: Record<string, NodeGPUDevice[]> = {}
    await Promise.all(
      n
        .filter((x) => (x.gpu_count || 0) > 0 && x.state === 'online')
        .map(async (x) => {
          try { next[x.id] = await listNodeGPUs(x.id) } catch {}
        }),
    )
    gpuAllocs.value = next
    lastPoll.value = fmtTime(new Date().toISOString())
  } catch {}
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

const online = computed(() => nodes.value.filter((n) => n.state === 'online'))
const offline = computed(() => nodes.value.filter((n) => n.state !== 'online'))
const totalCores = computed(() => nodes.value.reduce((s, n) => s + (n.cpu_cores || 0), 0))
const totalGpus = computed(() => nodes.value.reduce((s, n) => s + (n.gpu_count || 0), 0))

function ctrsByState(nodeId: string) {
  const buckets = { running: 0, starting: 0, stopping: 0, exited: 0, error: 0, total: 0 }
  for (const c of containers.value) {
    if (c.node_id !== nodeId) continue
    buckets.total += 1
    if (c.state in buckets) (buckets as any)[c.state] += 1
  }
  return buckets
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('page.nodes.title') }}</h1>
        <p class="nodes-header">
          <span class="kpi is-online"><strong>{{ online.length }}</strong>online</span>
          <span class="kpi is-offline"><strong>{{ offline.length }}</strong>offline</span>
          <span class="kpi"><strong>{{ totalCores }}</strong>{{ t('common.cores') }}</span>
          <span class="kpi"><strong>{{ totalGpus }}</strong>GPUs</span>
          <span class="kpi"><strong>{{ containers.length }}</strong>containers</span>
          <span class="dot" />
          <span class="mono" id="last-poll" style="font: 500 var(--fs-sm)/1.2 var(--font-mono); color: var(--text-dim);">{{ lastPoll }}</span>
        </p>
      </div>
      <div class="page-actions">
        <button class="btn is-primary" type="button" @click="router.push('/containers/new')">
          <span class="ico" aria-hidden="true">＋</span>
          <span>{{ t('containers.buttonNew') }}</span>
        </button>
      </div>
    </header>

    <div v-if="!nodes.length" class="empty">
      <div class="ico" aria-hidden="true">▣</div>
      <p class="empty-title">{{ t('nodes.empty') }}</p>
      <p class="empty-hint">{{ t('nodes.emptyHint') }} <code class="code">pp agent --master=master:7000 --name=my-node</code></p>
    </div>

    <div v-else class="nodes-grid">
      <article
        v-for="n in nodes"
        :key="n.id"
        class="node-card"
        :class="{ 'is-offline': n.state !== 'online' }"
      >
        <!-- Header -->
        <div>
          <div class="between" style="align-items: baseline;">
            <div class="row-tight">
              <span class="status-dot" :class="n.state" aria-hidden="true" />
              <span class="node-card-name">{{ n.name }}</span>
            </div>
            <span class="badge" :class="n.state === 'online' ? 'is-success' : ''">{{ t('state.' + n.state, n.state) }}</span>
          </div>
          <div class="node-spec" style="margin-top: var(--sp-2);">
            <span v-if="n.ip" class="ip">{{ n.ip }}</span>
            <span v-if="n.ip" class="dot" />
            <span>{{ n.os }}/{{ n.arch }}</span>
            <span class="dot" />
            <span>agent v{{ n.agent_version }}</span>
            <span class="dot" />
            <span>{{ t('nodes.upFor', { t: fmtUptime(n.registered_at) }) }}</span>
          </div>
        </div>

        <!-- 6-cell metric strip -->
        <div class="metric-strip">
          <div class="metric-cell is-cpu">
            <p class="lbl"><span class="swatch" />CPU</p>
            <p class="val">{{ Math.round(n.cpu_percent || 0) }}<span class="unit">%</span></p>
            <p class="sub">{{ n.cpu_cores }}c · {{ n.cpu_model || '—' }}</p>
            <div class="bar"><div class="bar-fill" :style="{ width: (n.cpu_percent || 0) + '%' }" /></div>
          </div>
          <div class="metric-cell is-mem">
            <p class="lbl"><span class="swatch" />{{ t('nodes.memory') }}</p>
            <p class="val">{{ n.mem_total_bytes ? Math.round((n.mem_used_bytes / n.mem_total_bytes) * 100) : 0 }}<span class="unit">%</span></p>
            <p class="sub">{{ fmtBytes(n.mem_used_bytes || 0) }} / {{ fmtBytes(n.mem_total_bytes || 0) }}</p>
            <div class="bar"><div class="bar-fill" :style="{ width: (n.mem_total_bytes ? (n.mem_used_bytes / n.mem_total_bytes) * 100 : 0) + '%' }" /></div>
          </div>
          <div class="metric-cell is-load">
            <p class="lbl"><span class="swatch" />{{ t('nodes.load') }}</p>
            <p class="val">{{ (n.load1 || 0).toFixed(2) }} <span class="small">/ {{ (n.load5 || 0).toFixed(2) }}</span></p>
            <p class="sub">{{ t('nodes.avgLabel', { a: t('nodes.oneMin'), b: t('nodes.fiveMin') }) }}</p>
          </div>
          <div class="metric-cell is-gpu">
            <p class="lbl"><span class="swatch" />GPU</p>
            <p class="val">
              <template v-if="(n.gpu_count || 0) > 0">{{ Math.round(n.gpu_usage_percent || 0) }}<span class="unit">%</span></template>
              <template v-else>{{ t('common.none') }}</template>
            </p>
            <p class="sub">{{ (n.gpu_count || 0) > 0 ? t('nodes.gpuFree', { free: n.gpu_free || 0, total: n.gpu_count }) : t('nodes.noneGpus') }}</p>
            <div v-if="(n.gpu_count || 0) > 0" class="bar"><div class="bar-fill" :style="{ width: (n.gpu_usage_percent || 0) + '%' }" /></div>
          </div>
          <div class="metric-cell is-net">
            <p class="lbl"><span class="swatch" />{{ t('nodes.netIo') }}</p>
            <p class="val">{{ fmtBytes((n.net_rx_bytes || 0) + (n.net_tx_bytes || 0)) }}</p>
            <p class="sub">↓ {{ fmtBytes(n.net_rx_bytes || 0) }} · ↑ {{ fmtBytes(n.net_tx_bytes || 0) }}</p>
          </div>
          <div class="metric-cell is-disk">
            <p class="lbl"><span class="swatch" />{{ t('nodes.disks') }}</p>
            <p class="val">{{ n.disks && n.disks.length ? n.disks.length : '—' }}</p>
            <p class="sub">{{ n.disks && n.disks.length ? t('nodes.diskTop', { pct: Math.max(...n.disks.map((d) => d.usage_percent)) }) : t('nodes.diskCountNone') }}</p>
          </div>
        </div>

        <!-- GPUs table -->
        <div v-if="n.state === 'online' && n.gpus && n.gpus.length">
          <div class="group-head">{{ t('nodes.gpus') }} <span class="tag is-gpu is-mono" style="margin-left:6px;">{{ n.gpus.length }} {{ t('common.devices') }}</span></div>
          <table class="gpu-table">
            <thead>
              <tr>
                <th>#</th>
                <th>Device</th>
                <th class="num">Mem</th>
                <th class="num">Util</th>
                <th>{{ t('containers.colState') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in n.gpus" :key="g.uuid || g.index">
                <td class="idx">{{ g.index }}</td>
                <td class="name">
                  <span>{{ g.name || 'Unknown' }}</span>
                  <span class="gpu-model">{{ g.uuid || '' }}</span>
                </td>
                <td class="num">{{ fmtBytes(g.mem_total_bytes || 0) }}</td>
                <td class="num">
                  <span :class="gpuAllocs[n.id]?.find((d) => d.index === g.index)?.held_by ? 'is-held' : 'is-free'">
                    {{ gpuAllocs[n.id]?.find((d) => d.index === g.index)?.held_by ? t('nodes.gpuBusy') : t('nodes.gpuIdle') }}
                  </span>
                </td>
                <td>
                  <span v-if="gpuAllocs[n.id]?.find((d) => d.index === g.index)?.held_by" class="held-by">
                    <span class="lbl">by</span>
                    {{ gpuAllocs[n.id]?.find((d) => d.index === g.index)?.held_by?.container_name || '—' }}
                  </span>
                  <span v-else class="free-tag">{{ t('nodes.gpuFreeTag') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Disks table -->
        <div v-if="n.disks && n.disks.length">
          <div class="group-head">{{ t('nodes.disks') }} <span class="tag is-mono" style="margin-left:6px;">{{ t('nodes.diskCount', { n: n.disks.length, s: n.disks.length === 1 ? '' : 's' }) }}</span></div>
          <table class="disk-table">
            <tbody>
              <tr v-for="d in n.disks" :key="d.path">
                <td class="path">{{ d.path }}</td>
                <td>
                  <div class="bar">
                    <div class="bar-fill" :style="{ width: d.usage_percent + '%', background: d.usage_percent >= 80 ? 'var(--danger)' : 'var(--metric-net)' }" />
                  </div>
                </td>
                <td class="num">{{ d.usage_percent }}%</td>
                <td class="total">{{ fmtBytes(d.used_bytes) }} / {{ fmtBytes(d.total_bytes) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Containers summary -->
        <div>
          <div class="group-head">{{ t('nodes.containers') }} <span class="tag is-mono" style="margin-left:6px;">{{ t('nodes.ctrTotal', { n: ctrsByState(n.id).total }) }}</span></div>
          <div v-if="ctrsByState(n.id).total === 0" class="ctr-summary">
            <span class="empty">{{ t('nodes.noContainers') }}</span>
          </div>
          <div v-else class="ctr-summary">
            <span v-if="ctrsByState(n.id).running" class="count is-running">
              <span class="status-dot running" />{{ t('nodes.ctrRunning', { n: ctrsByState(n.id).running }) }}
            </span>
            <span v-if="ctrsByState(n.id).starting" class="count is-creating">
              <span class="status-dot starting" />{{ t('nodes.ctrStarting', { n: ctrsByState(n.id).starting }) }}
            </span>
            <span v-if="ctrsByState(n.id).stopping" class="count is-stopping">
              <span class="status-dot stopping" />{{ t('nodes.ctrStopping', { n: ctrsByState(n.id).stopping }) }}
            </span>
            <span v-if="ctrsByState(n.id).exited" class="count is-exited">
              <span class="status-dot exited" />{{ t('nodes.ctrExited', { n: ctrsByState(n.id).exited }) }}
            </span>
            <span v-if="ctrsByState(n.id).error" class="count is-error">
              <span class="status-dot error" />{{ t('nodes.ctrError', { n: ctrsByState(n.id).error }) }}
            </span>
          </div>
        </div>

        <!-- Footer -->
        <div class="node-foot">
          <span class="item">{{ t('nodes.heartbeat') }}<strong> {{ fmtAge(n.last_heartbeat) }}</strong></span>
          <span class="sep">·</span>
          <span class="item">{{ t('nodes.metrics') }}<strong> {{ fmtAge(n.metrics_at) }}</strong></span>
          <span class="sep">·</span>
          <span class="item">{{ t('nodes.runtime') }}<strong> {{ n.runtime || '—' }}</strong></span>
        </div>
      </article>
    </div>

    <p class="faint mono" style="text-align:center; margin-top: var(--sp-9); font-size: var(--fs-xs);">
      {{ t('nodes.perGpuFooter') }}
    </p>
  </div>
</template>

<style scoped>
.nodes-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--sp-4);
}
@media (max-width: 1000px) { .nodes-grid { grid-template-columns: 1fr; } }

.node-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--r-4);
  padding: var(--sp-5);
  display: flex;
  flex-direction: column;
  gap: var(--sp-4);
  min-width: 0;
}
.node-card.is-offline { opacity: 0.78; }

.node-spec {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px var(--sp-3);
  font: 500 var(--fs-sm)/1.4 var(--font-body);
  color: var(--text-dim);
  font-variant-numeric: tabular-nums;
}
.node-spec .dot {
  width: 3px; height: 3px; border-radius: 50%;
  background: var(--text-faint); display: inline-block;
}
.node-spec .ip { font-family: var(--font-mono); }

/* 6-cell metric strip */
.metric-strip {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 1px;
  background: var(--border-soft);
  border: 1px solid var(--border-soft);
  border-radius: var(--r-3);
  overflow: hidden;
}
.metric-cell {
  background: var(--bg-soft);
  padding: var(--sp-3);
  display: flex; flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.metric-cell .lbl {
  font: 600 var(--fs-xs)/1 var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-dim);
  display: flex; align-items: center; gap: 5px;
}
.metric-cell .lbl .swatch {
  width: 6px; height: 6px; border-radius: 2px; background: var(--text-faint);
}
.metric-cell.is-cpu .lbl .swatch { background: var(--metric-cpu); }
.metric-cell.is-mem .lbl .swatch { background: var(--metric-mem); }
.metric-cell.is-load .lbl .swatch { background: var(--metric-net); }
.metric-cell.is-gpu .lbl .swatch { background: var(--metric-gpu); }
.metric-cell.is-net .lbl .swatch { background: var(--metric-net); }
.metric-cell.is-disk .lbl .swatch { background: var(--warn); }

.metric-cell .val {
  font: 700 var(--fs-lg)/1.1 var(--font-display);
  letter-spacing: -0.015em;
  color: var(--text);
  font-variant-numeric: tabular-nums;
  display: flex; align-items: baseline; gap: 3px;
  min-width: 0;
}
.metric-cell .val .unit { font-size: 0.55em; color: var(--text-dim); font-weight: 600; }
.metric-cell .val .small { font-size: 0.6em; color: var(--text-dim); font-weight: 600; }
.metric-cell .sub {
  font: 500 var(--fs-xs)/1.2 var(--font-mono);
  color: var(--text-dim);
  font-variant-numeric: tabular-nums;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.metric-cell .bar { height: 3px; margin-top: 4px; background: var(--bg-inset); border-radius: var(--r-pill); overflow: hidden; }
.metric-cell .bar-fill { height: 100%; width: 0; transition: width var(--t-slow); }
.metric-cell.is-cpu .bar-fill { background: var(--metric-cpu); }
.metric-cell.is-mem .bar-fill { background: var(--metric-mem); }
.metric-cell.is-gpu .bar-fill { background: var(--metric-gpu); }

@media (max-width: 1200px) { .metric-strip { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 700px)  { .metric-strip { grid-template-columns: repeat(2, minmax(0, 1fr)); } }

/* GPU table */
.gpu-table {
  width: 100%;
  border-collapse: collapse;
  font: 500 var(--fs-sm)/1.4 var(--font-body);
}
.gpu-table th {
  text-align: left;
  font: 600 var(--fs-xs)/1 var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-dim);
  padding: 0 var(--sp-2) 4px;
  border-bottom: 1px solid var(--border-soft);
}
.gpu-table th.num { text-align: right; }
.gpu-table td {
  padding: 5px var(--sp-2);
  border-bottom: 1px solid var(--border-soft);
  color: var(--text);
  vertical-align: middle;
}
.gpu-table tr:last-child td { border-bottom: 0; }
.gpu-table .idx { color: var(--text-dim); font: 600 var(--fs-sm)/1 var(--font-mono); width: 28px; }
.gpu-table .name .gpu-model { display: block; color: var(--text-dim); font-size: var(--fs-xs); font-family: var(--font-mono); }
.gpu-table .num { text-align: right; font-variant-numeric: tabular-nums; font-family: var(--font-mono); color: var(--text-dim); }
.gpu-table .num.is-held { color: var(--metric-gpu); }
.gpu-table .num.is-free { color: var(--success); }
.gpu-table .held-by { font: 500 var(--fs-xs)/1.2 var(--font-mono); color: var(--metric-gpu); }
.gpu-table .held-by .lbl { color: var(--text-faint); margin-right: 4px; }
.gpu-table .free-tag { color: var(--success); font: 600 var(--fs-xs)/1.2 var(--font-mono); }

/* Disk table */
.disk-table { width: 100%; border-collapse: collapse; font: 500 var(--fs-sm)/1.4 var(--font-body); }
.disk-table td { padding: 4px var(--sp-2); color: var(--text-dim); border-bottom: 1px solid var(--border-soft); }
.disk-table tr:last-child td { border-bottom: 0; }
.disk-table .path { color: var(--text); font-family: var(--font-mono); width: 80px; }
.disk-table .num { text-align: right; font-variant-numeric: tabular-nums; font-family: var(--font-mono); width: 70px; color: var(--text); }
.disk-table .total { text-align: right; font-variant-numeric: tabular-nums; font-family: var(--font-mono); width: 130px; color: var(--text-dim); }

/* Container summary */
.ctr-summary {
  display: flex; align-items: center; flex-wrap: wrap;
  gap: 6px;
}
.ctr-summary .count {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 8px;
  border-radius: var(--r-pill);
  font: 600 var(--fs-xs)/1.4 var(--font-mono);
  border: 1px solid var(--border-soft);
  background: var(--bg-elev);
  color: var(--text-dim);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.ctr-summary .count.is-running  { color: var(--success); border-color: color-mix(in oklab, var(--success) 35%, transparent); background: var(--success-soft); }
.ctr-summary .count.is-creating { color: var(--accent);  border-color: color-mix(in oklab, var(--accent)  35%, transparent); background: var(--accent-soft); }
.ctr-summary .count.is-stopping { color: var(--warn);    border-color: color-mix(in oklab, var(--warn)    35%, transparent); background: var(--warn-soft); }
.ctr-summary .count.is-error    { color: var(--danger);  border-color: color-mix(in oklab, var(--danger)  35%, transparent); background: var(--danger-soft); }
.ctr-summary .empty { color: var(--text-faint); font: 500 var(--fs-sm)/1.4 var(--font-body); }

.node-foot {
  display: flex; flex-wrap: wrap; align-items: center;
  gap: 4px var(--sp-4);
  padding-top: var(--sp-3);
  border-top: 1px solid var(--border-soft);
  font: 500 var(--fs-xs)/1.3 var(--font-mono);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-faint);
}
.node-foot .item { color: var(--text-dim); }
.node-foot .item strong { color: var(--text); font-weight: 600; }
.node-foot .sep { color: var(--text-faint); }

.nodes-header {
  display: flex; flex-wrap: wrap; align-items: center;
  gap: var(--sp-4);
}
.nodes-header .kpi {
  display: inline-flex; align-items: baseline; gap: 6px;
  font: 500 var(--fs-sm)/1.2 var(--font-mono);
  color: var(--text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.nodes-header .kpi strong {
  color: var(--text);
  font: 700 var(--fs-lg)/1 var(--font-display);
  letter-spacing: -0.01em;
  font-variant-numeric: tabular-nums;
}
.nodes-header .kpi.is-online strong { color: var(--success); }
.nodes-header .kpi.is-offline strong { color: var(--text-dim); }
</style>
