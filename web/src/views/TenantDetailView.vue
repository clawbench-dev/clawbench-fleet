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
  Square,
  Trash2,
  Wrench,
} from 'lucide-vue-next'
import { api, downloadFile } from '../api'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CopyField from '../components/CopyField.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { toast } from '../stores/toast'
import type { AuditEntry, Backup, Tenant } from '../types'
import { actionLabel, formatBytes, formatTime } from '../utils'

const route = useRoute()
const router = useRouter()
const id = computed(() => Number(route.params.id))

const tenant = ref<Tenant | null>(null)
const backups = ref<Backup[]>([])
const audit = ref<AuditEntry[]>([])
const loading = ref(true)
const error = ref('')
const tab = ref<'logs' | 'backups' | 'audit'>('logs')
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

const quota = ref({ cpu_quota: '', memory_max: '', tasks_max: 0 })

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
    }
    error.value = ''
  } catch (e: any) {
    error.value = e?.message || '加载失败'
  } finally {
    if (!silent) loading.value = false
  }
}

onMounted(async () => {
  await load()
  timer = window.setInterval(() => load(true), 5000)
})
onBeforeUnmount(() => window.clearInterval(timer))

watch(tab, (value) => {
  if (value === 'audit') loadAudit()
})

async function act(action: 'start' | 'stop' | 'restart') {
  const labels = { start: '启动', stop: '停止', restart: '重启' }
  busy.value = action
  try {
    const res = await api.post<{ tenant: Tenant }>(`/tenants/${id.value}/${action}`)
    tenant.value = res.tenant
    toast(`${labels[action]}成功`, 'success')
  } catch (e: any) {
    toast(e?.message || `${labels[action]}失败`, 'error')
  } finally {
    busy.value = ''
    load(true)
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
  } catch (e: any) {
    toast(e?.message || '重置密码失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '修复失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '保存失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '读取日志失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '读取操作记录失败', 'error')
  }
}

async function backupNow() {
  busy.value = 'backup'
  try {
    await api.post(`/tenants/${id.value}/backups`)
    toast('备份完成', 'success')
    load(true)
  } catch (e: any) {
    toast(e?.message || '备份失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '恢复失败', 'error')
  }
}

async function doRemoveBackup() {
  if (!removeBackupTarget.value) return
  try {
    await api.del(`/tenants/${id.value}/backups/${removeBackupTarget.value.id}`)
    toast('备份已删除', 'success')
    removeBackupTarget.value = null
    load(true)
  } catch (e: any) {
    toast(e?.message || '删除失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '删除失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '强制删除失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '恢复失败', 'error')
  } finally {
    busy.value = ''
  }
}

function download(backup: Backup) {
  downloadFile(`/backups/${backup.id}/download`, `${backup.username}-${backup.id}.tar.gz`).catch((e) =>
    toast(e?.message || '下载失败', 'error'),
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
          <div class="muted small" style="margin-top: 3px">
            {{ tenant.display_name || '未填写显示名' }}
            <span v-if="tenant.notes"> · {{ tenant.notes }}</span>
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
          <button class="btn btn-danger" type="button" :disabled="!!busy" @click="deleteOpen = true">
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
          <div class="detail-item"><span class="detail-label">端口</span><span class="detail-value mono">{{ tenant.port }}</span></div>
          <div class="detail-item"><span class="detail-label">版本</span><span class="detail-value mono">{{ tenant.version || '—' }}</span></div>
          <div class="detail-item"><span class="detail-label">UID / GID</span><span class="detail-value mono">{{ tenant.uid }} / {{ tenant.gid }}</span></div>
          <div class="detail-item"><span class="detail-label">创建时间</span><span class="detail-value">{{ formatTime(tenant.created_at) }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">家目录</span><span class="detail-value mono">{{ tenant.home_dir }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">数据目录</span><span class="detail-value mono">{{ tenant.data_dir }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">systemd 单元</span><span class="detail-value mono">{{ tenant.unit_name }}</span></div>
          <div class="detail-item"><span class="detail-label">最后健康检查</span><span class="detail-value">{{ formatTime(tenant.last_health_at) }}</span></div>
          <div class="detail-item"><span class="detail-label">磁盘占用</span><span class="detail-value">{{ formatBytes(tenant.disk_bytes) }}</span></div>
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
