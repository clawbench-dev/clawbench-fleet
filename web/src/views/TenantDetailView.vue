<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  AlertTriangle,
  Download,
  FileText,
  KeyRound,
  Play,
  RefreshCw,
  RotateCcw,
  RotateCw,
  ScrollText,
  Activity,
  Square,
  Trash2,
  Wrench,
} from 'lucide-vue-next'
import { api, downloadFile, errorMessage } from '../api'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CopyField from '../components/CopyField.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import LineChart from '../components/LineChart.vue'
import StatusBadge from '../components/StatusBadge.vue'
import type { Series } from '../components/LineChart.vue'
import { toast } from '../stores/toast'
import type { AuditEntry, Backup, MetricSample, SSHStatus, Tenant } from '../types'
import { actionLabel, formatAgo, formatBytes, formatPercent, formatTime } from '../utils'

const route = useRoute()
const router = useRouter()
const id = computed(() => Number(route.params.id))

const tenant = ref<Tenant | null>(null)
const backups = ref<Backup[]>([])
const audit = ref<AuditEntry[]>([])
const loading = ref(true)
const error = ref('')
const tab = ref<'logs' | 'backups' | 'usage' | 'audit'>('logs')
const logs = ref('')
const logsLoading = ref(false)
const busy = ref('')
const password = ref('')
const passwordTitle = ref('新密码')
const deleteOpen = ref(false)
const deleteDoneOpen = ref(false)
const forceDeleteOpen = ref(false)
const restoreTarget = ref<Backup | null>(null)
const removeBackupTarget = ref<Backup | null>(null)
const trashed = computed(() => tenant.value?.status === 'trashed')
const ssh = ref<{ status: SSHStatus; host: string } | null>(null)
const sshDisableOpen = ref(false)
const sshCommand = computed(() => {
  if (!tenant.value?.ssh_enabled || !ssh.value) return ''
  const port = ssh.value.status.port || 22
  const portPart = port === 22 ? '' : ` -p ${port}`
  return `ssh${portPart} ${tenant.value.username}@${ssh.value.host}`
})

const quota = ref({ cpu_quota: '', memory_max: '', tasks_max: 0, disk_quota_gb: 0 })
const usageWindow = ref<'24h' | '7d'>('24h')
const usage = ref<MetricSample[]>([])
const usageLoading = ref(false)
const usageError = ref('')

const cpuSeries = computed<Series[]>(() => [
  { name: 'CPU %', color: 'var(--accent)', values: usage.value.map((s) => s.cpu_percent) },
])
const memorySeries = computed<Series[]>(() => [
  { name: '内存', color: 'var(--purple)', values: usage.value.map((s) => s.memory_bytes) },
])
const diskSeries = computed<Series[]>(() => [
  { name: '磁盘', color: 'var(--amber)', values: usage.value.map((s) => s.disk_bytes) },
])
const usageLabels = computed(() =>
  usage.value.map((s) => {
    const d = new Date(s.at)
    return usageWindow.value === '24h'
      ? d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
      : d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' })
  }),
)
const portOpen = ref(false)
const portDraft = ref(0)
const portError = ref('')
const profileOpen = ref(false)
const profileDraft = ref({ display_name: '', notes: '' })
const profileError = ref('')

let timer = 0

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const res = await api.get<{ tenant: Tenant; backups: Backup[] }>(`/tenants/${id.value}`)
    tenant.value = res.tenant
    backups.value = res.backups || []
    quota.value = {
      cpu_quota: res.tenant.cpu_quota,
      memory_max: res.tenant.memory_max,
      tasks_max: res.tenant.tasks_max,
      disk_quota_gb: res.tenant.disk_quota_gb,
    }
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, '加载失败')
  } finally {
    if (!silent) loading.value = false
  }
}

onMounted(async () => {
  await load()
  await loadSSH()
  timer = window.setInterval(() => load(true), 5000)
})
onBeforeUnmount(() => window.clearInterval(timer))

watch(tab, (value) => {
  if (value === 'audit') loadAudit()
  if (value === 'usage') loadUsage()
})

watch(usageWindow, () => {
  if (tab.value === 'usage') loadUsage()
})

const diskMeasuring = ref(false)

// A never-sampled tenant has disk_checked_at empty and disk_bytes 0. Showing
// "0 B" there is a lie: it means "not measured yet", which is a different
// thing from an empty home directory.
const diskText = computed(() => {
  const t = tenant.value
  if (!t) return '—'
  if (!t.disk_checked_at) return '未统计'
  return formatBytes(t.disk_bytes)
})

async function refreshDisk() {
  if (diskMeasuring.value) return
  diskMeasuring.value = true
  busy.value = 'disk'
  try {
    const res = await api.post<{ tenant: Tenant }>(`/tenants/${id.value}/disk/refresh`, {})
    tenant.value = res.tenant
    toast('磁盘统计已更新', 'success')
  } catch (e) {
    toast(errorMessage(e, '统计磁盘失败'), 'error')
  } finally {
    diskMeasuring.value = false
    busy.value = ''
  }
}

async function loadUsage() {
  usageLoading.value = true
  usageError.value = ''
  try {
    const window = usageWindow.value === '24h' ? '24h' : '168h'
    const res = await api.get<{ items: MetricSample[] }>(`/tenants/${id.value}/metrics/history?window=${window}`)
    usage.value = res.items || []
  } catch (e) {
    usageError.value = errorMessage(e, '加载用量失败')
  } finally {
    usageLoading.value = false
  }
}

async function act(action: 'start' | 'stop' | 'restart') {
  const labels = { start: '启动', stop: '停止', restart: '重启' }
  busy.value = action
  try {
    const res = await api.post<{ tenant: Tenant }>(`/tenants/${id.value}/${action}`)
    tenant.value = res.tenant
    toast(`${labels[action]}成功`, 'success')
  } catch (e) {
    toast(errorMessage(e, `${labels[action]}失败`), 'error')
  } finally {
    busy.value = ''
    load(true)
  }
}

async function loadSSH() {
  try {
    ssh.value = await api.get<{ status: SSHStatus; host: string }>('/ssh/status')
  } catch {
    ssh.value = null
  }
}

async function setSSH(enabled: boolean) {
  busy.value = 'ssh'
  try {
    const res = await api.post<{ tenant: Tenant; password?: string; warning?: string }>(
      `/tenants/${id.value}/ssh`,
      { enabled },
    )
    tenant.value = res.tenant
    if (res.password) {
      password.value = res.password
      passwordTitle.value = 'SSH 已开启，网页和 SSH 的新密码（只显示这一次）'
    } else {
      toast('SSH 已关闭', 'success')
    }
    if (res.warning) toast(res.warning, 'error')
    else if (res.password) toast('SSH 已开启', 'success')
    await loadSSH()
    load(true)
  } catch (e) {
    toast(errorMessage(e, enabled ? '开启 SSH 失败' : '关闭 SSH 失败'), 'error')
  } finally {
    busy.value = ''
    sshDisableOpen.value = false
  }
}

async function resetPassword() {
  busy.value = 'reset'
  try {
    const res = await api.post<{ tenant: Tenant; password: string; warning?: string }>(
      `/tenants/${id.value}/reset-password`,
    )
    tenant.value = res.tenant
    password.value = res.password
    passwordTitle.value = '新的登录密码（只显示这一次）'
    if (res.warning) toast(res.warning, 'error')
  } catch (e) {
    toast(errorMessage(e, '重置密码失败'), 'error')
  } finally {
    busy.value = ''
    load(true)
  }
}

async function repair() {
  busy.value = 'repair'
  try {
    const res = await api.post<{ tenant: Tenant; password?: string; warning?: string }>(
      `/tenants/${id.value}/repair`,
    )
    tenant.value = res.tenant
    if (res.password) {
      password.value = res.password
      passwordTitle.value = '修复时生成了新密码（只显示这一次）'
    } else {
      toast('修复完成', 'success')
    }
    if (res.warning) toast(res.warning, 'error')
  } catch (e) {
    toast(errorMessage(e, '修复失败'), 'error')
  } finally {
    busy.value = ''
    load(true)
  }
}

async function saveQuota() {
  busy.value = 'quota'
  try {
    const res = await api.patch<{ tenant: Tenant }>(`/tenants/${id.value}`, quota.value)
    tenant.value = res.tenant
    toast('资源上限已保存，实例已重启', 'success')
  } catch (e) {
    toast(errorMessage(e, '保存失败'), 'error')
  } finally {
    busy.value = ''
    load(true)
  }
}

function openPortEdit() {
  portDraft.value = tenant.value?.port || 0
  portError.value = ''
  portOpen.value = true
}

function openProfileEdit() {
  profileDraft.value = {
    display_name: tenant.value?.display_name || '',
    notes: tenant.value?.notes || '',
  }
  profileError.value = ''
  profileOpen.value = true
}

async function saveProfile() {
  if (!tenant.value) return
  profileError.value = ''
  busy.value = 'profile'
  try {
    const res = await api.patch<{ tenant: Tenant }>(`/tenants/${id.value}`, {
      display_name: profileDraft.value.display_name,
      notes: profileDraft.value.notes,
    })
    tenant.value = res.tenant
    profileOpen.value = false
    toast('租户资料已保存', 'success')
  } catch (e) {
    profileError.value = errorMessage(e, '保存失败')
  } finally {
    busy.value = ''
    load(true)
  }
}

async function savePort() {
  if (!tenant.value) return
  const port = Number(portDraft.value)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    portError.value = '端口必须是 1-65535 之间的整数'
    return
  }
  if (port === tenant.value.port) {
    portError.value = '新端口和当前端口一样'
    return
  }
  portError.value = ''
  busy.value = 'port'
  try {
    const res = await api.patch<{ tenant: Tenant }>(`/tenants/${id.value}`, { port })
    tenant.value = res.tenant
    portOpen.value = false
    toast(
      res.tenant.status === 'running'
        ? `端口已改为 ${port}，实例已按新端口重启`
        : `端口已改为 ${port}，下次启动生效`,
      'success',
    )
  } catch (e) {
    portError.value = errorMessage(e, '修改端口失败')
  } finally {
    busy.value = ''
    load(true)
  }
}

async function loadLogs() {
  logsLoading.value = true
  try {
    const res = await api.get<{ lines: string }>(`/tenants/${id.value}/logs?lines=500`)
    logs.value = res.lines || '（日志为空）'
  } catch (e) {
    toast(errorMessage(e, '读取日志失败'), 'error')
  } finally {
    logsLoading.value = false
  }
}

async function loadAudit() {
  if (!tenant.value) return
  try {
    const res = await api.get<{ items: AuditEntry[] }>(
      `/audit?tenant=${encodeURIComponent(tenant.value.username)}&page_size=30`,
    )
    audit.value = res.items
  } catch (e) {
    toast(errorMessage(e, '读取操作记录失败'), 'error')
  }
}

async function backupNow() {
  busy.value = 'backup'
  try {
    await api.post(`/tenants/${id.value}/backups`)
    toast('备份完成', 'success')
    load(true)
  } catch (e) {
    toast(errorMessage(e, '备份失败'), 'error')
  } finally {
    busy.value = ''
  }
}

async function doRestore() {
  if (!restoreTarget.value) return
  try {
    await api.post(`/tenants/${id.value}/backups/${restoreTarget.value.id}/restore`)
    toast('已从备份恢复', 'success')
    restoreTarget.value = null
    load()
  } catch (e) {
    toast(errorMessage(e, '恢复失败'), 'error')
  }
}

async function doRemoveBackup() {
  if (!removeBackupTarget.value) return
  try {
    await api.del(`/tenants/${id.value}/backups/${removeBackupTarget.value.id}`)
    toast('备份已删除', 'success')
    removeBackupTarget.value = null
    load(true)
  } catch (e) {
    toast(errorMessage(e, '删除失败'), 'error')
  }
}

async function doDelete() {
  if (!tenant.value) return
  deleteOpen.value = false
  try {
    const res = await api.del<{ tenant: Tenant }>(`/tenants/${id.value}`, { confirm: tenant.value.username })
    tenant.value = res.tenant
    toast('已移入回收站，30 天内可以恢复', 'success')
    deleteDoneOpen.value = true
    load(true)
  } catch (e) {
    toast(errorMessage(e, '删除失败'), 'error')
  }
}

function finishDelete() {
  deleteDoneOpen.value = false
  router.push('/tenants')
}

async function doForceDelete() {
  if (!tenant.value) return
  try {
    await api.del(`/trash/${tenant.value.id}`)
    forceDeleteOpen.value = false
    toast('已强制删除，数据不可恢复', 'success')
    router.push('/tenants')
  } catch (e) {
    toast(errorMessage(e, '强制删除失败'), 'error')
  }
}

async function restoreFromTrash() {
  busy.value = 'restore'
  try {
    const res = await api.post<{ tenant: Tenant; password?: string; warning?: string }>(
      `/trash/${id.value}/restore`,
    )
    tenant.value = res.tenant
    if (res.password) {
      password.value = res.password
      passwordTitle.value = '恢复时生成了新密码（只显示这一次）'
    } else {
      toast('租户已恢复', 'success')
    }
    if (res.warning) toast(res.warning, 'error')
    load(true)
  } catch (e) {
    toast(errorMessage(e, '恢复失败'), 'error')
  } finally {
    busy.value = ''
  }
}

function download(backup: Backup) {
  downloadFile(`/backups/${backup.id}/download`, `${backup.username}-${backup.id}.tar.gz`).catch((e) =>
    toast(errorMessage(e, '下载失败'), 'error'),
  )
}
</script>

<template>
  <div v-if="error" class="card" style="border-color: rgba(255,107,107,.4)">
    <div class="row"><AlertTriangle :size="18" color="var(--red)" /> {{ error }}</div>
  </div>

  <div v-else-if="loading && !tenant" class="muted">加载中…</div>

  <div v-else-if="tenant">
    <div class="card">
      <div class="row-between" style="flex-wrap: wrap; gap: 14px">
        <div style="min-width: 240px">
          <div class="row" style="gap: 10px">
            <h1 class="mono" style="font-size: 22px">{{ tenant.username }}</h1>
            <StatusBadge :status="tenant.status" />
          </div>
          <div class="row muted small" style="margin-top: 3px; gap: 8px; flex-wrap: wrap">
            <span>
              {{ tenant.display_name || '未填写显示名' }}
              <span v-if="tenant.notes"> · {{ tenant.notes }}</span>
            </span>
            <button
              v-if="!trashed"
              class="btn btn-ghost btn-sm"
              type="button"
              :disabled="!!busy"
              @click="openProfileEdit"
            >
              编辑资料
            </button>
          </div>
        </div>

        <div v-if="!trashed" class="btn-row">
          <button
            v-if="tenant.status === 'running'"
            class="btn"
            type="button"
            :disabled="!!busy"
            @click="act('stop')"
          >
            <Square :size="15" /> 停止
          </button>
          <button v-else class="btn" type="button" :disabled="!!busy" @click="act('start')">
            <Play :size="15" /> 启动
          </button>
          <button class="btn" type="button" :disabled="!!busy" @click="act('restart')">
            <RotateCw :size="15" /> 重启
          </button>
          <button class="btn" type="button" :disabled="!!busy" @click="resetPassword">
            <KeyRound :size="15" /> 重置密码
          </button>
          <button class="btn" type="button" :disabled="!!busy" @click="repair">
            <Wrench :size="15" /> 修复
          </button>
          <button
            class="btn btn-danger"
            type="button"
            :disabled="!!busy || tenant.protected"
            :title="tenant.protected ? 'root 是系统账号，不能删除' : ''"
            @click="deleteOpen = true"
          >
            <Trash2 :size="15" /> 删除
          </button>
        </div>
        <div v-else class="btn-row">
          <button class="btn btn-primary" type="button" :disabled="!!busy" @click="restoreFromTrash">
            <RotateCcw :size="15" /> 恢复租户
          </button>
          <button class="btn btn-danger" type="button" :disabled="!!busy" @click="forceDeleteOpen = true">
            <Trash2 :size="15" /> 强制删除
          </button>
        </div>
      </div>

      <div v-if="trashed" class="card" style="margin-top: 14px; padding: 12px; border-color: rgba(179,136,255,.4)">
        <div class="row small" style="color: var(--purple)">
          <Trash2 :size="15" />
          这个租户在回收站，默认保留 30 天后自动清理。可以恢复，也可以立即强制删除（不可恢复）。
        </div>
      </div>

      <div v-if="tenant.protected && !trashed" class="row small muted-2" style="margin-top: 12px">
        <AlertTriangle :size="15" />
        root 是系统账号，平台只做启停和日常运维，不会删除它。
      </div>

      <div style="margin-top: 16px; max-width: 520px">
        <CopyField label="员工访问地址" :value="tenant.address" />
      </div>

      <div v-if="tenant.last_error" class="card" style="margin-top: 14px; padding: 12px; border-color: rgba(255,107,107,.35)">
        <div class="row small" style="color: #ffb3b3"><AlertTriangle :size="15" /> {{ tenant.last_error }}</div>
      </div>
    </div>

    <div class="grid grid-2" style="margin-top: 16px">
      <div class="card">
        <div class="card-header"><div class="card-title">基本信息</div></div>
        <div class="detail-grid">
          <div class="detail-item">
            <span class="detail-label">端口</span>
            <span class="row" style="gap: 8px">
              <span class="detail-value mono">{{ tenant.port }}</span>
              <button
                v-if="!trashed"
                class="btn btn-ghost btn-sm"
                type="button"
                :disabled="!!busy"
                @click="openPortEdit"
              >
                修改
              </button>
            </span>
          </div>
          <div class="detail-item"><span class="detail-label">版本</span><span class="detail-value mono">{{ tenant.version || '—' }}</span></div>
          <div class="detail-item"><span class="detail-label">UID / GID</span><span class="detail-value mono">{{ tenant.uid }} / {{ tenant.gid }}</span></div>
          <div class="detail-item"><span class="detail-label">创建时间</span><span class="detail-value">{{ formatTime(tenant.created_at) }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">家目录</span><span class="detail-value mono">{{ tenant.home_dir }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">数据目录</span><span class="detail-value mono">{{ tenant.data_dir }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">systemd 单元</span><span class="detail-value mono">{{ tenant.unit_name }}</span></div>
          <div class="detail-item"><span class="detail-label">最后健康检查</span><span class="detail-value">{{ formatTime(tenant.last_health_at) }}</span></div>
          <div class="detail-item"><span class="detail-label">最近活跃</span><span class="detail-value">{{ formatAgo(tenant.last_active_at) }}</span></div>
          <div class="detail-item">
            <span class="detail-label">磁盘占用</span>
            <span class="detail-value row" style="gap: 8px; flex-wrap: wrap">
              <span>{{ diskText }}</span>
              <button
                v-if="!trashed"
                class="btn btn-ghost btn-sm"
                type="button"
                :disabled="diskMeasuring"
                @click="refreshDisk"
              >
                <RefreshCw :size="13" :class="{ spin: diskMeasuring }" />
                {{ diskMeasuring ? '统计中…' : '立即统计' }}
              </button>
            </span>
            <span v-if="tenant.disk_checked_at" class="detail-label" style="font-weight: 400">
              上次统计：{{ formatAgo(tenant.disk_checked_at) }}
            </span>
          </div>
          <div class="detail-item" style="grid-column: span 2">
            <span class="detail-label">SSH 登录</span>
            <span class="row" style="gap: 8px; flex-wrap: wrap">
              <span class="badge" :class="tenant.ssh_enabled ? 'badge-ok' : ''">
                {{ tenant.ssh_enabled ? '已开启' : '未开启' }}
              </span>
              <button
                v-if="!trashed"
                class="btn btn-ghost btn-sm"
                type="button"
                :disabled="!!busy"
                @click="tenant.ssh_enabled ? (sshDisableOpen = true) : setSSH(true)"
              >
                {{ busy === 'ssh' ? '处理中…' : tenant.ssh_enabled ? '关闭 SSH' : '开启 SSH' }}
              </button>
            </span>
          </div>
        </div>
        <div v-if="tenant.ssh_enabled && sshCommand" style="margin-top: 12px">
          <CopyField label="SSH 登录命令" :value="sshCommand" />
        </div>
        <div
          v-if="tenant.ssh_enabled && ssh?.status.problem"
          class="row small"
          style="color: var(--amber); margin-top: 10px"
        >
          <AlertTriangle :size="15" /> {{ ssh.status.problem }}
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">资源上限</div>
            <div class="card-sub">{{ trashed ? '租户在回收站，恢复后才能修改' : '保存后会自动重启实例' }}</div>
          </div>
        </div>
        <div class="stack">
          <div class="field"><label>CPU 上限</label><input v-model="quota.cpu_quota" class="input mono" placeholder="200%" :disabled="trashed" /></div>
          <div class="field"><label>内存上限</label><input v-model="quota.memory_max" class="input mono" placeholder="4G" :disabled="trashed" /></div>
          <div class="field"><label>进程数上限</label><input v-model.number="quota.tasks_max" class="input mono" type="number" min="1" :disabled="trashed" /></div>
          <div class="field">
            <label>磁盘上限（GB，0 = 不限制）</label>
            <input v-model.number="quota.disk_quota_gb" class="input mono" type="number" min="0" :disabled="trashed" />
          </div>
        </div>
        <div v-if="!trashed" class="form-actions">
          <button class="btn btn-primary" type="button" :disabled="busy === 'quota'" @click="saveQuota">
            {{ busy === 'quota' ? '保存中…' : '保存并重启' }}
          </button>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="tabs">
        <button class="tab" :class="{ active: tab === 'logs' }" type="button" @click="tab = 'logs'">
          <FileText :size="14" style="vertical-align: -2px" /> 日志
        </button>
        <button class="tab" :class="{ active: tab === 'backups' }" type="button" @click="tab = 'backups'">
          <Download :size="14" style="vertical-align: -2px" /> 备份（{{ backups.length }}）
        </button>
        <button class="tab" :class="{ active: tab === 'usage' }" type="button" @click="tab = 'usage'">
          <Activity :size="14" style="vertical-align: -2px" /> 用量
        </button>
        <button class="tab" :class="{ active: tab === 'audit' }" type="button" @click="tab = 'audit'">
          <ScrollText :size="14" style="vertical-align: -2px" /> 操作记录
        </button>
      </div>

      <div v-if="tab === 'logs'">
        <div class="row-between" style="margin-bottom: 10px">
          <span class="small muted-2">最近 500 行，来自 journalctl</span>
          <button class="btn btn-sm" type="button" :disabled="logsLoading" @click="loadLogs">
            <RefreshCw :size="14" :class="{ spin: logsLoading }" /> {{ logsLoading ? '读取中…' : '加载日志' }}
          </button>
        </div>
        <pre class="pre">{{ logs || '还没有加载日志。点右上角“加载日志”。' }}</pre>
      </div>

      <div v-else-if="tab === 'backups'">
        <div class="row-between" style="margin-bottom: 10px">
          <span class="small muted-2">每天自动备份数据库和配置，保留最近若干天</span>
          <button class="btn btn-sm btn-primary" type="button" :disabled="busy === 'backup'" @click="backupNow">
            {{ busy === 'backup' ? '备份中…' : '立即备份' }}
          </button>
        </div>
        <div v-if="backups.length" class="table-wrap">
          <table class="table">
            <thead><tr><th>时间</th><th>大小</th><th>完整性</th><th>状态</th><th style="text-align: right">操作</th></tr></thead>
            <tbody>
              <tr v-for="b in backups" :key="b.id">
                <td class="small">{{ formatTime(b.created_at) }}</td>
                <td class="small num">{{ formatBytes(b.size_bytes) }}</td>
                <td><span class="badge" :class="b.integrity_ok ? 'badge-ok' : 'badge-failed'">{{ b.integrity_ok ? '通过' : '未通过' }}</span></td>
                <td><span class="badge" :class="b.status === 'ok' ? 'badge-ok' : 'badge-failed'">{{ b.status === 'ok' ? '成功' : '失败' }}</span></td>
                <td>
                  <div class="actions">
                    <button class="btn btn-ghost btn-sm btn-icon" type="button" title="下载" @click="download(b)"><Download :size="14" /></button>
                    <button class="btn btn-sm" type="button" :disabled="b.status !== 'ok'" @click="restoreTarget = b">恢复</button>
                    <button class="btn btn-ghost btn-sm btn-icon" type="button" title="删除" @click="removeBackupTarget = b"><Trash2 :size="14" /></button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <EmptyState v-else title="还没有备份" hint="点“立即备份”生成第一份，或者等每天凌晨的自动备份。" />
      </div>

      <div v-else-if="tab === 'usage'">
        <div class="row-between" style="margin-bottom: 12px; flex-wrap: wrap; gap: 10px">
          <span class="small muted-2">每 5 分钟记一个点，最多查最近 7 天</span>
          <div class="btn-row">
            <button class="btn btn-sm" :class="{ 'btn-primary': usageWindow === '24h' }" type="button" @click="usageWindow = '24h'">最近 24 小时</button>
            <button class="btn btn-sm" :class="{ 'btn-primary': usageWindow === '7d' }" type="button" @click="usageWindow = '7d'">最近 7 天</button>
            <button class="btn btn-sm" type="button" :disabled="usageLoading" @click="loadUsage">
              <RefreshCw :size="14" :class="{ spin: usageLoading }" /> 刷新
            </button>
          </div>
        </div>
        <div v-if="usageError" class="small" style="color: var(--red); margin-bottom: 10px">{{ usageError }}</div>
        <div v-if="!usage.length && !usageLoading" class="muted small" style="padding: 20px 0">
          还没有用量记录。实例运行满 5 分钟后就会出现第一个点。
        </div>
        <div v-else class="stack" style="gap: 22px">
          <div>
            <div class="card-title" style="margin-bottom: 6px">CPU</div>
            <LineChart :labels="usageLabels" :series="cpuSeries" :format="(v) => formatPercent(v)" />
          </div>
          <div>
            <div class="card-title" style="margin-bottom: 6px">内存</div>
            <LineChart :labels="usageLabels" :series="memorySeries" :format="(v) => formatBytes(v)" />
          </div>
          <div>
            <div class="card-title" style="margin-bottom: 6px">磁盘</div>
            <LineChart :labels="usageLabels" :series="diskSeries" :format="(v) => formatBytes(v)" />
          </div>
        </div>
      </div>

      <div v-else>
        <div v-if="audit.length" class="table-wrap">
          <table class="table">
            <thead><tr><th>时间</th><th>操作</th><th>操作人</th><th>结果</th><th>IP</th></tr></thead>
            <tbody>
              <tr v-for="a in audit" :key="a.id">
                <td class="small">{{ formatTime(a.created_at) }}</td>
                <td class="small">{{ actionLabel(a.action) }}</td>
                <td class="small mono">{{ a.actor }}</td>
                <td><span class="badge" :class="a.result === 'ok' ? 'badge-ok' : 'badge-failed'">{{ a.result === 'ok' ? '成功' : '失败' }}</span></td>
                <td class="small mono">{{ a.ip }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <EmptyState v-else title="还没有操作记录" hint="对这个租户的每次操作都会记录在这里。" />
      </div>
    </div>
  </div>

  <ConfirmDialog
    :open="deleteOpen"
    title="删除租户"
    :message="`确定要删除 ${tenant?.username} 吗？\n\n服务会停止，Linux 账号会删除，整个家目录会移进回收站保留 30 天。\n删除完成后可以立即强制删除，也可以等 30 天后自动清理。`"
    :require-text="tenant?.username"
    confirm-text="删除并移入回收站"
    danger
    @cancel="deleteOpen = false"
    @confirm="doDelete"
  />

  <Modal v-if="deleteDoneOpen" title="已移入回收站" @close="finishDelete">
    <p class="muted" style="white-space: pre-wrap">
      租户已经删除，数据在回收站里保留 30 天。{{ '\n\n' }}现在可以立即强制删除（不可恢复），也可以先留着，之后在“回收站”里处理。
    </p>
    <template #footer>
      <button class="btn" type="button" @click="finishDelete">稍后处理</button>
      <button
        class="btn btn-danger"
        type="button"
        @click="deleteDoneOpen = false; forceDeleteOpen = true"
      >
        立即强制删除
      </button>
    </template>
  </Modal>

  <ConfirmDialog
    :open="forceDeleteOpen"
    title="强制删除（跳过 30 天）"
    :message="`立即永久删除 ${tenant?.username} 的家目录、项目代码和所有数据，不能恢复。\n\n这个操作只在租户已经移入回收站后可用。`"
    :require-text="tenant?.username"
    confirm-text="永久删除"
    danger
    @cancel="forceDeleteOpen = false"
    @confirm="doForceDelete"
  />

  <ConfirmDialog
    :open="!!restoreTarget"
    title="从备份恢复"
    :message="`将用 ${restoreTarget ? formatTime(restoreTarget.created_at) : ''} 的数据库覆盖当前数据。\n\n实例会先停止再启动，配置保持不变。恢复前会自动保留一份当前数据库。`"
    confirm-text="确认恢复"
    danger
    @cancel="restoreTarget = null"
    @confirm="doRestore"
  />

  <ConfirmDialog
    :open="!!removeBackupTarget"
    title="删除备份"
    message="删除后这份备份就不能再用来恢复了。确定吗？"
    confirm-text="删除"
    danger
    @cancel="removeBackupTarget = null"
    @confirm="doRemoveBackup"
  />

  <Modal v-if="profileOpen" title="编辑租户资料" @close="profileOpen = false">
    <div class="stack">
      <div class="field">
        <label>显示名</label>
        <input v-model="profileDraft.display_name" class="input" placeholder="例如 张三（研发部）" />
      </div>
      <div class="field">
        <label>备注</label>
        <input v-model="profileDraft.notes" class="input" placeholder="例如 研发部，工位 A12" />
      </div>
    </div>
    <div v-if="profileError" class="small" style="color: var(--red); margin-top: 8px">{{ profileError }}</div>
    <template #footer>
      <button class="btn" type="button" @click="profileOpen = false">取消</button>
      <button class="btn btn-primary" type="button" :disabled="busy === 'profile'" @click="saveProfile">
        {{ busy === 'profile' ? '保存中…' : '保存' }}
      </button>
    </template>
  </Modal>

  <Modal v-if="portOpen" title="修改端口" @close="portOpen = false">
    <p class="muted small" style="margin-top: 0">
      当前端口是 <span class="mono">{{ tenant?.port }}</span>。改完后平台会重写实例配置和 systemd 单元，运行中的实例会重启一次；员工访问地址会变成新端口，记得通知员工。
    </p>
    <div class="field">
      <label>新端口</label>
      <input
        v-model.number="portDraft"
        class="input mono"
        type="number"
        min="1"
        max="65535"
        placeholder="例如 21001"
      />
    </div>
    <div v-if="portError" class="small" style="color: var(--red); margin-top: 8px">{{ portError }}</div>
    <template #footer>
      <button class="btn" type="button" @click="portOpen = false">取消</button>
      <button class="btn btn-primary" type="button" :disabled="busy === 'port'" @click="savePort">
        {{ busy === 'port' ? '保存中…' : '保存修改' }}
      </button>
    </template>
  </Modal>

  <ConfirmDialog
    :open="sshDisableOpen"
    title="关闭 SSH 登录"
    message="关闭后，这个账号会改回不能登录的 shell，密码也会被锁定，员工不能再通过 SSH 进入服务器。网页访问和现有密码不受影响。"
    confirm-text="关闭 SSH"
    danger
    @cancel="sshDisableOpen = false"
    @confirm="setSSH(false)"
  />

  <Modal v-if="password" :title="passwordTitle" @close="password = ''">
    <CopyField label="新密码" :value="password" />
    <div class="small muted-2" style="margin-top: 12px">关闭后就不能再看到这个密码，请先复制发给员工。</div>
    <template #footer>
      <button class="btn btn-primary" type="button" @click="password = ''">我已保存</button>
    </template>
  </Modal>
</template>

<style scoped>
.spin {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
