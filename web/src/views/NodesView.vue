<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { fmtBytes, fmtTime, listNodes, type Node } from '@/api/client'

const { t } = useI18n()
const nodes = ref<Node[]>([])
let timer: number | undefined

async function refresh() {
  try {
    nodes.value = await listNodes()
  } catch (e) {
    console.warn(e)
  }
}
onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => timer && clearInterval(timer))

const online = computed(() => nodes.value.filter((n) => n.state === 'online'))
const offline = computed(() => nodes.value.filter((n) => n.state !== 'online'))

function stateLabel(s: string) {
  return t(`state.${s}` as any, s)
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('nodes.title') }}</h1>
        <div class="page-subtitle">
          {{ t('nodes.online', { n: online.length }) }} · {{ t('nodes.offline', { n: offline.length }) }}
        </div>
      </div>
    </div>

    <div v-if="!nodes.length" style="text-align:center;color:var(--text-dim);padding:48px 0;">
      <el-icon :size="48"><Cpu /></el-icon>
      <div style="margin-top:12px;">{{ t('nodes.empty') }}</div>
      <div style="margin-top:8px;font-size:12px;">
        {{ t('nodes.emptyHint') }} <code>pp agent --master=master:7000 --name=my-node</code>
      </div>
    </div>

    <div v-else class="cards">
      <el-card v-for="n in nodes" :key="n.id">
        <template #header>
          <div style="display:flex;align-items:center;justify-content:space-between;">
            <div>
              <span :class="['status-dot', n.state]"></span>
              <strong>{{ n.name }}</strong>
            </div>
            <el-tag size="small" :type="n.state==='online' ? 'success' : 'info'">
              {{ stateLabel(n.state) }}
            </el-tag>
          </div>
        </template>

        <div style="font-size:12px;color:var(--text-dim);">
          {{ n.hostname }} · {{ n.os }} · {{ n.arch }} · v{{ n.agent_version }}
        </div>

        <div class="metric-grid">
          <div class="metric">
            <div class="metric-label">{{ t('nodes.cpu') }}</div>
            <div class="metric-value">{{ n.cpu_percent.toFixed(1) }}%</div>
            <div class="metric-bar"><span :style="{ width: Math.min(n.cpu_percent,100)+'%' }"></span></div>
            <div style="font-size:11px;color:var(--text-dim);margin-top:4px;">{{ n.cpu_cores }} {{ t('unit.cores') }}</div>
          </div>
          <div class="metric">
            <div class="metric-label">{{ t('nodes.memory') }}</div>
            <div class="metric-value">
              {{ n.mem_total_bytes>0 ? ((n.mem_used_bytes/n.mem_total_bytes)*100).toFixed(1) : 0 }}%
            </div>
            <div class="metric-bar"><span :style="{ width: (n.mem_total_bytes>0?(n.mem_used_bytes/n.mem_total_bytes)*100:0)+'%' }"></span></div>
            <div style="font-size:11px;color:var(--text-dim);margin-top:4px;">
              {{ fmtBytes(n.mem_used_bytes) }} / {{ fmtBytes(n.mem_total_bytes) }}
            </div>
          </div>
          <div class="metric">
            <div class="metric-label">{{ t('nodes.load') }}</div>
            <div class="metric-value">{{ n.load1.toFixed(2) }}</div>
          </div>
          <div class="metric">
            <div class="metric-label">{{ t('nodes.gpu') }}</div>
            <div class="metric-value">
              {{ n.gpu_count > 0 ? n.gpu_usage_percent.toFixed(1)+'%' : t('common.none') }}
            </div>
            <div v-if="n.gpu_count > 0" style="font-size:11px;color:var(--text-dim);margin-top:4px;">
              {{ n.gpu_count }} GPU · {{ fmtBytes(n.gpu_mem_used_bytes) }} / {{ fmtBytes(n.gpu_mem_total_bytes) }}
            </div>
          </div>
        </div>

        <div v-if="n.disks && n.disks.length" style="margin-top:12px;">
          <div style="color:var(--text-dim);font-size:12px;margin-bottom:4px;">{{ t('nodes.disks') }}</div>
          <div v-for="d in n.disks.slice(0,4)" :key="d.path" style="margin-bottom:6px;">
            <div style="display:flex;justify-content:space-between;font-size:12px;">
              <span>{{ d.path }}</span>
              <span>{{ d.usage_percent.toFixed(1) }}%</span>
            </div>
            <div class="metric-bar"><span :style="{ width: Math.min(d.usage_percent,100)+'%' }"></span></div>
            <div style="font-size:10px;color:var(--text-dim);">
              {{ fmtBytes(d.used_bytes) }} / {{ fmtBytes(d.total_bytes) }}
            </div>
          </div>
        </div>

        <div v-if="n.gpus && n.gpus.length" style="margin-top:12px;">
          <div style="color:var(--text-dim);font-size:12px;margin-bottom:4px;">{{ t('nodes.gpus') }}</div>
          <div v-for="g in n.gpus" :key="g.uuid" style="margin-bottom:6px;font-size:12px;">
            <div style="display:flex;justify-content:space-between;">
              <span>GPU {{ g.index }}: {{ g.name }}</span>
              <span>{{ g.usage_percent.toFixed(1) }}%</span>
            </div>
            <div class="metric-bar"><span :style="{ width: Math.min(g.usage_percent,100)+'%', background: 'linear-gradient(90deg,#ab47bc,#ff8a3d)' }"></span></div>
            <div style="font-size:10px;color:var(--text-dim);">
              {{ fmtBytes(g.mem_used_bytes) }} / {{ fmtBytes(g.mem_total_bytes) }}
            </div>
          </div>
        </div>

        <div style="font-size:11px;color:var(--text-dim);margin-top:10px;">
          {{ t('nodes.lastHeartbeat', { t: fmtTime(n.last_heartbeat) }) }} · {{ t('nodes.metricsAt', { t: fmtTime(n.metrics_at) }) }}
        </div>
      </el-card>
    </div>
  </div>
</template>
