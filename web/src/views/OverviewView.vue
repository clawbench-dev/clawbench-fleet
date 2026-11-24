<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  Activity,
  AlertTriangle,
  ArrowUpCircle,
  Clock,
  Cpu,
  HardDrive,
  Server,
  Users,
} from 'lucide-vue-next'
import { api, errorMessage } from '../api'
import EmptyState from '../components/EmptyState.vue'
import LoadingState from '../components/LoadingState.vue'
import MetricBar from '../components/MetricBar.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { toast } from '../stores/toast'
import type { Overview } from '../types'
import { actionLabel, formatBytes, formatPercent, formatTime } from '../utils'

const data = ref<Overview | null>(null)
const error = ref('')
const loading = ref(true)
let timer = 0

async function load() {
  try {
    data.value = await api.get<Overview>('/overview')
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, '加载失败')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onBeforeUnmount(() => window.clearInterval(timer))

async function resolveReport(id: number) {
  try {
    await api.post(`/reports/${id}/resolve`)
    toast('已标记为处理完', 'success')
    load()
  } catch (e) {
    toast(errorMessage(e, '操作失败'), 'error')
  }
}

const counts = computed(() => data.value?.counts || {})
const running = computed(() => counts.value.running || 0)
const stopped = computed(() => counts.value.stopped || 0)
const failed = computed(() => counts.value.error || 0)

const memPercent = computed(() => {
  const h = data.value?.host
  if (!h || !h.mem_total) return 0
  return (h.mem_used / h.mem_total) * 100
})
const diskPercent = computed(() => {
  const h = data.value?.host
  if (!h || !h.disk_total) return 0
  return ((h.disk_total - h.disk_free) / h.disk_total) * 100
})
</script>

<template>
  <div>
    <div v-if="error" class="card" style="border-color: rgba(255,107,107,.4)">
      <div class="row"><AlertTriangle :size="18" color="var(--red)" /> {{ error }}</div>
    </div>

    <div v-if="loading" class="card">
      <LoadingState text="正在读取机器和实例状态…" />
    </div>

    <template v-else-if="data">
    <div class="grid grid-4">
      <div class="stat">
        <div class="stat-label"><Users :size="14" /> 租户总数</div>
        <div class="stat-value">{{ data?.total ?? '—' }}</div>
        <div class="stat-hint">已创建的实例</div>
      </div>
      <div class="stat">
        <div class="stat-label"><Activity :size="14" color="var(--green)" /> 运行中</div>
        <div class="stat-value" style="color: var(--green)">{{ running }}</div>
        <div class="stat-hint">健康检查通过</div>
      </div>
      <div class="stat">
        <div class="stat-label"><Server :size="14" /> 已停止</div>
        <div class="stat-value">{{ stopped }}</div>
        <div class="stat-hint">手动停止或停用</div>
      </div>
      <div class="stat">
        <div class="stat-label"><AlertTriangle :size="14" color="var(--red)" /> 异常</div>
        <div class="stat-value" :style="{ color: failed ? 'var(--red)' : 'var(--text)' }">{{ failed }}</div>
        <div class="stat-hint">需要处理</div>
      </div>
    </div>

    <div class="grid grid-3" style="margin-top: 16px">
      <div class="card">
        <div class="card-header">
          <div class="card-title"><Cpu :size="15" style="vertical-align: -2px" /> 机器 CPU</div>
          <span class="small mono muted">{{ formatPercent(data?.host.cpu_percent || 0) }}</span>
        </div>
        <MetricBar
          label="当前使用率"
          :value="formatPercent(data?.host.cpu_percent || 0)"
          :percent="data?.host.cpu_percent || 0"
          :tone="(data?.host.cpu_percent || 0) > 85 ? 'red' : (data?.host.cpu_percent || 0) > 60 ? 'amber' : 'green'"
        />
        <div class="small muted-2" style="margin-top: 12px">负载 {{ data?.host.load1?.toFixed(2) ?? '—' }}</div>
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title"><HardDrive :size="15" style="vertical-align: -2px" /> 内存</div>
          <span class="small mono muted">{{ formatBytes(data?.host.mem_used || 0) }} / {{ formatBytes(data?.host.mem_total || 0) }}</span>
        </div>
        <MetricBar
          label="已使用"
          :value="formatPercent(memPercent)"
          :percent="memPercent"
          :tone="memPercent > 85 ? 'red' : memPercent > 70 ? 'amber' : 'green'"
        />
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title"><Server :size="15" style="vertical-align: -2px" /> 磁盘</div>
          <span class="small mono muted">{{ formatBytes((data?.host.disk_total || 0) - (data?.host.disk_free || 0)) }} / {{ formatBytes(data?.host.disk_total || 0) }}</span>
        </div>
        <MetricBar
          label="已使用"
          :value="formatPercent(diskPercent)"
          :percent="diskPercent"
          :tone="diskPercent > 90 ? 'red' : diskPercent > 75 ? 'amber' : 'green'"
        />
      </div>
    </div>

    <div v-if="data?.disk_warnings?.length" class="card" style="margin-top: 16px; border-color: rgba(245,185,76,.4)">
      <div class="card-header">
        <div>
          <div class="card-title">磁盘占用提醒</div>
          <div class="card-sub">家目录超过提醒阈值的租户；不会自动停服务，请人工确认</div>
        </div>
        <span class="badge badge-neutral">{{ data.disk_warnings.length }} 个</span>
      </div>
      <div class="table-wrap">
        <table class="table">
          <thead><tr><th>租户</th><th>磁盘占用</th><th>地址</th><th></th></tr></thead>
          <tbody>
            <tr v-for="t in data.disk_warnings" :key="t.id">
              <td><span class="mono">{{ t.username }}</span><div class="small muted-2">{{ t.display_name }}</div></td>
              <td class="num small" style="color: var(--amber)">{{ formatBytes(t.disk_bytes) }}</td>
              <td class="small mono">{{ t.address }}</td>
              <td class="actions"><router-link class="btn btn-sm" :to="`/tenants/${t.id}`">查看</router-link></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="data?.guest_reports?.length" class="card" style="margin-top: 16px; border-color: rgba(91, 140, 255, 0.35)">
      <div class="card-header">
        <div>
          <div class="card-title">员工报障</div>
          <div class="card-sub">员工在自助页点的「打不开」，处理完点一下「已处理」</div>
        </div>
        <span class="badge badge-neutral">{{ data.open_reports }} 条待处理</span>
      </div>
      <div class="table-wrap">
        <table class="table">
          <thead><tr><th>员工</th><th>说明</th><th>时间</th><th>来源 IP</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in data.guest_reports" :key="r.id">
              <td>
                <router-link v-if="r.tenant_id" :to="`/tenants/${r.tenant_id}`">{{ r.tenant_name }}</router-link>
                <span v-else>{{ r.tenant_name }}</span>
              </td>
              <td class="small" style="max-width: 360px">{{ r.message || '（没填）' }}</td>
              <td class="small muted-2">{{ formatTime(r.created_at) }}</td>
              <td class="small mono muted-2">{{ r.ip }}</td>
              <td class="actions">
                <button class="btn btn-sm" type="button" @click="resolveReport(r.id)">已处理</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">需要处理的实例</div>
          <div class="card-sub">健康检查失败、服务起不来或单元丢失的租户</div>
        </div>
        <span class="badge" :class="failed ? 'badge-error' : 'badge-ok'">{{ failed ? `${failed} 个异常` : '全部正常' }}</span>
      </div>
      <div v-if="data?.anomalies?.length" class="table-wrap">
        <table class="table">
          <thead>
            <tr><th>租户</th><th>状态</th><th>最近错误</th><th>地址</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="t in data.anomalies" :key="t.id">
              <td><span class="mono">{{ t.username }}</span><div class="small muted-2">{{ t.display_name }}</div></td>
              <td><StatusBadge :status="t.status" /></td>
              <td class="small" style="max-width: 360px">{{ t.last_error || '—' }}</td>
              <td class="small mono">{{ t.address }}</td>
              <td class="actions"><router-link class="btn btn-sm" :to="`/tenants/${t.id}`">查看</router-link></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty" style="padding: 22px">没有异常实例，机器状态正常。</div>
    </div>

    <div class="grid grid-2" style="margin-top: 16px">
      <div class="card">
        <div class="card-header">
          <div class="card-title">最近操作</div>
          <router-link class="small" to="/audit">全部记录</router-link>
        </div>
        <div v-if="data?.recent_audit?.length" class="stack">
          <div v-for="a in data.recent_audit" :key="a.id" class="row-between">
            <div style="min-width: 0">
              <div class="small">
                <strong>{{ actionLabel(a.action) }}</strong>
                <span v-if="a.tenant_name" class="muted"> · {{ a.tenant_name }}</span>
              </div>
              <div class="small muted-2">{{ a.actor }} · {{ formatTime(a.created_at) }}</div>
            </div>
            <span class="badge" :class="a.result === 'ok' ? 'badge-ok' : 'badge-failed'">{{ a.result === 'ok' ? '成功' : '失败' }}</span>
          </div>
        </div>
        <div v-else class="empty" style="padding: 18px">还没有操作记录。</div>
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title"><ArrowUpCircle :size="15" style="vertical-align: -2px" /> 版本与任务</div>
          <router-link class="small" to="/upgrades">升级中心</router-link>
        </div>
        <div class="row-between" style="margin-bottom: 12px">
          <span class="small muted">当前版本</span>
          <span class="tag">{{ data?.current_version || '未上传' }}</span>
        </div>
        <div v-if="data?.recent_jobs?.length" class="stack">
          <div v-for="j in data.recent_jobs" :key="j.id" class="row-between">
            <div class="small">
              <strong>{{ j.type }}</strong>
              <span class="muted-2"> · {{ j.done }}/{{ j.total }}</span>
            </div>
            <span class="badge" :class="j.status === 'succeeded' ? 'badge-ok' : j.status === 'failed' ? 'badge-failed' : 'badge-neutral'">{{ j.status }}</span>
          </div>
        </div>
        <div v-else class="empty" style="padding: 18px">还没有批量任务。</div>
        <div v-if="data?.orphans?.length" class="small" style="margin-top: 12px; color: var(--amber)">
          发现 {{ data.orphans.length }} 个孤儿 systemd 单元：{{ data.orphans.join('、') }}
        </div>
      </div>
    </div>

    <div v-if="data && data.total === 0" class="card" style="margin-top: 16px">
      <EmptyState title="还没有租户" hint="先上传一个 Clawbench 版本，然后创建第一个实例。">
        <div class="btn-row" style="justify-content: center">
          <router-link class="btn" to="/upgrades">去上传版本</router-link>
          <router-link class="btn btn-primary" to="/tenants/new">新建租户</router-link>
        </div>
      </EmptyState>
    </div>

    <div class="small muted-2" style="margin-top: 18px; display: flex; gap: 16px; flex-wrap: wrap">
      <span><Clock :size="13" style="vertical-align: -2px" /> 每 5 秒自动刷新</span>
      <span v-if="data?.mode === 'dev'" style="color: var(--amber)">开发模式：使用模拟的 systemd 和实例，不会真的建 Linux 用户</span>
    </div>
    </template>
  </div>
</template>
