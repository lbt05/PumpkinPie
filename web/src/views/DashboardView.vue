<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart, GaugeChart, BarChart } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  TitleComponent,
  LegendComponent,
} from 'echarts/components'
import { listContainers, listNodes, fmtBytes, fmtTime, type Container, type Node } from '@/api/client'

use([CanvasRenderer, LineChart, GaugeChart, BarChart, GridComponent, TooltipComponent, TitleComponent, LegendComponent])

const { t } = useI18n()
const nodes = ref<Node[]>([])
const containers = ref<Container[]>([])
let timer: number | undefined

async function refresh() {
  try {
    ;[nodes.value, containers.value] = await Promise.all([listNodes(), listContainers()])
  } catch (e) {
    console.warn(e)
  }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => timer && clearInterval(timer))

const stats = computed(() => {
  const totalCores = nodes.value.reduce((s, n) => s + n.cpu_cores, 0)
  const totalMem = nodes.value.reduce((s, n) => s + n.mem_total_bytes, 0)
  const usedMem = nodes.value.reduce((s, n) => s + n.mem_used_bytes, 0)
  const totalGpu = nodes.value.reduce((s, n) => s + n.gpu_count, 0)
  return {
    nodes: nodes.value.length,
    online: nodes.value.filter((n) => n.state === 'online').length,
    containers: containers.value.length,
    running: containers.value.filter((c) => c.state === 'running' || c.state === 'created').length,
    totalCores,
    totalMem,
    usedMem,
    totalGpu,
  }
})

function avgUsage(): number {
  if (!nodes.value.length) return 0
  return nodes.value.reduce((s, n) => s + n.cpu_percent, 0) / nodes.value.length
}

function memPercent(): number {
  if (stats.value.totalMem === 0) return 0
  return (stats.value.usedMem / stats.value.totalMem) * 100
}

function gpuPercent(): number {
  const totals = nodes.value.reduce((s, n) => s + n.gpu_count, 0)
  if (totals === 0) return 0
  const used = nodes.value.reduce(
    (s, n) => s + (n.gpu_count > 0 ? n.gpu_usage_percent * n.gpu_count : 0),
    0,
  )
  return used / totals
}

const cpuGauge = computed(() => ({
  series: [
    {
      type: 'gauge',
      progress: { show: true, width: 12 },
      axisLine: { lineStyle: { width: 12 } },
      pointer: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { show: false },
      detail: { valueAnimation: true, fontSize: 24, color: 'var(--text)', formatter: '{value}%' },
      data: [{ value: Number(avgUsage().toFixed(1)) }],
    },
  ],
}))

const memGauge = computed(() => ({
  series: [
    {
      type: 'gauge',
      progress: { show: true, width: 12 },
      axisLine: { lineStyle: { width: 12 } },
      pointer: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { show: false },
      detail: { valueAnimation: true, fontSize: 24, color: 'var(--text)', formatter: '{value}%' },
      data: [{ value: Number(memPercent().toFixed(1)) }],
    },
  ],
}))

const gpuGauge = computed(() => ({
  series: [
    {
      type: 'gauge',
      progress: { show: true, width: 12 },
      axisLine: { lineStyle: { width: 12 } },
      pointer: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { show: false },
      detail: { valueAnimation: true, fontSize: 24, color: 'var(--text)', formatter: '{value}%' },
      data: [{ value: Number(gpuPercent().toFixed(1)) }],
    },
  ],
}))

const perNodeBar = computed(() => {
  const online = nodes.value.filter((n) => n.state === 'online')
  return {
    tooltip: { trigger: 'axis' },
    grid: { left: 30, right: 20, top: 30, bottom: 50 },
    xAxis: {
      type: 'category',
      data: online.map((n) => n.name),
      axisLabel: { color: 'var(--text-dim)', rotate: 30 },
    },
    yAxis: {
      type: 'value',
      max: 100,
      axisLabel: { color: 'var(--text-dim)', formatter: '{value}%' },
      splitLine: { lineStyle: { color: 'var(--border)' } },
    },
    series: [
      {
        name: 'CPU',
        type: 'bar',
        data: online.map((n) => Number(n.cpu_percent.toFixed(1))),
        itemStyle: { color: '#ff8a3d' },
      },
      {
        name: 'GPU',
        type: 'bar',
        data: online.map((n) => Number(n.gpu_usage_percent.toFixed(1))),
        itemStyle: { color: '#ab47bc' },
      },
      {
        name: 'MEM',
        type: 'bar',
        data: online.map((n) =>
          n.mem_total_bytes > 0
            ? Number(((n.mem_used_bytes / n.mem_total_bytes) * 100).toFixed(1))
            : 0,
        ),
        itemStyle: { color: '#42a5f5' },
      },
    ],
    legend: { textStyle: { color: 'var(--text-dim)' } },
  }
})

function stateLabel(s: string) {
  return t(`state.${s}` as any, s)
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('dashboard.title') }}</h1>
        <div class="page-subtitle">{{ t('dashboard.subtitle') }}</div>
      </div>
    </div>

    <div class="cards">
      <el-card>
        <div style="display:flex;justify-content:space-between;align-items:center;">
          <div>
            <div style="color:var(--text-dim);font-size:12px;">{{ t('dashboard.cardNodes') }}</div>
            <div style="font-size:28px;font-weight:700;">{{ stats.online }} / {{ stats.nodes }}</div>
            <div style="font-size:12px;color:var(--text-dim);">
              {{ t('dashboard.cardNodesDetail', { cores: stats.totalCores, mem: fmtBytes(stats.totalMem), gpu: stats.totalGpu }) }}
            </div>
          </div>
          <el-icon :size="32" color="var(--accent)"><Cpu /></el-icon>
        </div>
      </el-card>
      <el-card>
        <div style="display:flex;justify-content:space-between;align-items:center;">
          <div>
            <div style="color:var(--text-dim);font-size:12px;">{{ t('dashboard.cardContainers') }}</div>
            <div style="font-size:28px;font-weight:700;">{{ stats.running }} / {{ stats.containers }}</div>
            <div style="font-size:12px;color:var(--text-dim);">{{ t('dashboard.cardContainersDetail') }}</div>
          </div>
          <el-icon :size="32" color="var(--blue)"><Box /></el-icon>
        </div>
      </el-card>
    </div>

    <div class="cards" style="grid-template-columns:repeat(auto-fill,minmax(320px,1fr));margin-top:16px;">
      <el-card>
        <template #header>{{ t('dashboard.gaugeClusterCpu') }}</template>
        <v-chart :option="cpuGauge" style="height:180px;" autoresize />
      </el-card>
      <el-card>
        <template #header>{{ t('dashboard.gaugeClusterMemory') }}</template>
        <v-chart :option="memGauge" style="height:180px;" autoresize />
        <div style="font-size:12px;color:var(--text-dim);text-align:center;">
          {{ t('dashboard.memText', { used: fmtBytes(stats.usedMem), total: fmtBytes(stats.totalMem) }) }}
        </div>
      </el-card>
      <el-card>
        <template #header>{{ t('dashboard.gaugeClusterGpu') }}</template>
        <v-chart :option="gpuGauge" style="height:180px;" autoresize />
        <div style="font-size:12px;color:var(--text-dim);text-align:center;">
          {{ t('dashboard.gpusAcrossCluster', { n: stats.totalGpu }) }}
        </div>
      </el-card>
    </div>

    <el-card style="margin-top:16px;">
      <template #header>{{ t('dashboard.perNodeUtilization') }}</template>
      <v-chart :option="perNodeBar" style="height:280px;" autoresize />
    </el-card>

    <el-card style="margin-top:16px;">
      <template #header>{{ t('dashboard.recentContainers') }}</template>
      <el-table :data="containers.slice(0, 8)" stripe>
        <el-table-column :label="t('dashboard.colName')" prop="name" />
        <el-table-column :label="t('dashboard.colImage')" prop="image" />
        <el-table-column :label="t('dashboard.colNode')" prop="node_name" />
        <el-table-column :label="t('dashboard.colState')">
          <template #default="{ row }">
            <span :class="['status-dot', row.state]"></span>{{ stateLabel(row.state) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('dashboard.colExternal')">
          <template #default="{ row }">
            <a v-if="row.external_url" :href="row.external_url" target="_blank">:{{ row.external_port }}</a>
            <span v-else>{{ t('common.na') }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('dashboard.colCreated')">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>
