<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { KeyRound, Save, Server, ShieldCheck } from 'lucide-vue-next'
import { api, errorMessage } from '../api'
import Modal from '../components/Modal.vue'
import { auth } from '../stores/auth'
import { toast } from '../stores/toast'
import type { CertificateInfo, ConfigEntry, Job, Settings, SSHStatus } from '../types'

const router = useRouter()
const settings = ref<Settings | null>(null)
const tenantConfig = ref<ConfigEntry[]>([])
const cert = ref<CertificateInfo | null>(null)
const certSaving = ref(false)
const meta = ref<{
  mode: string
  public_host: string
  install_dir: string
  current_version: string
  fleet_version: string
  ssh_status: SSHStatus
} | null>(null)
const saving = ref(false)
const pwd = ref({ current_password: '', new_password: '', confirm: '' })
const pwdSaving = ref(false)

const sshReady = computed(
  () => !!meta.value?.ssh_status.installed && !!meta.value?.ssh_status.running && !!meta.value?.ssh_status.password_auth,
)

const guestURL = computed(() => window.location.origin + '/guest')

async function load() {
  try {
    const res = await api.get<{
      settings: Settings
      mode: string
      public_host: string
      install_dir: string
      current_version: string
      fleet_version: string
      ssh_status: SSHStatus
      certificate: CertificateInfo
      tenant_config: ConfigEntry[]
    }>('/settings')
    settings.value = res.settings
    tenantConfig.value = res.tenant_config || []
    cert.value = res.certificate
    meta.value = {
      mode: res.mode,
      public_host: res.public_host,
      install_dir: res.install_dir,
      current_version: res.current_version,
      fleet_version: res.fleet_version,
      ssh_status: res.ssh_status,
    }
  } catch (e) {
    toast(errorMessage(e, '加载失败'), 'error')
  }
}

const applyOpen = ref(false)
const applyStarting = ref(false)
const applyError = ref('')
const applyJob = ref<Job | null>(null)
let applyTimer = 0

onMounted(load)
onBeforeUnmount(() => window.clearInterval(applyTimer))

// persist saves the form and returns an error message, or '' on success. The
// save button shows a toast; the apply flow shows the message inside its modal
// instead, so a validation error does not disappear behind a popup.
async function persist(): Promise<string> {
  if (!settings.value) return '设置还没有加载完'
  saving.value = true
  try {
    const res = await api.patch<{ settings: Settings }>('/settings', settings.value)
    settings.value = res.settings
    return ''
  } catch (e) {
    return errorMessage(e, '保存失败')
  } finally {
    saving.value = false
  }
}

async function save() {
  const err = await persist()
  if (err) {
    toast(err, 'error')
    return
  }
  // The unified config lives in its own endpoint: it is a list, not a scalar
  // setting, and it is saved without touching the instances.
  try {
    const res = await api.put<{ tenant_config: ConfigEntry[] }>('/settings/tenant-config', {
      entries: tenantConfig.value.filter((e) => e.key.trim() || e.value.trim()),
    })
    tenantConfig.value = res.tenant_config
  } catch (e) {
    toast(errorMessage(e, '统一配置保存失败'), 'error')
    return
  }
  toast('设置已保存（尚未下发到实例）', 'success')
}

function addConfigEntry() {
  tenantConfig.value.push({ key: '', value: '' })
}

function removeConfigEntry(index: number) {
  tenantConfig.value.splice(index, 1)
}

async function regenerateCert() {
  certSaving.value = true
  try {
    const res = await api.post<{ certificate: CertificateInfo }>('/settings/cert/regenerate')
    cert.value = res.certificate
    toast('已重新生成证书，还需下发到实例才会生效', 'success')
  } catch (e) {
    toast(errorMessage(e, '生成证书失败'), 'error')
  } finally {
    certSaving.value = false
  }
}

function openApply() {
  // A running job keeps going in the background; reopening the dialog shows
  // its live progress instead of starting a second push.
  if (applyJob.value && !applyDone.value) {
    applyOpen.value = true
    return
  }
  applyError.value = ''
  applyJob.value = null
  applyOpen.value = true
}

function closeApply() {
  // Closing only hides the dialog: the server-side job keeps running and the
  // poll below still updates the button state and reports the result.
  applyOpen.value = false
}

async function startApply() {
  applyError.value = ''
  applyStarting.value = true
  try {
    const saveErr = await persist()
    if (saveErr) {
      applyError.value = saveErr
      return
    }
    const res = await api.post<{ job: Job }>('/settings/apply-quota')
    applyJob.value = res.job
    pollApply()
  } catch (e) {
    applyError.value = errorMessage(e, '应用失败')
  } finally {
    applyStarting.value = false
  }
}

function pollApply() {
  window.clearInterval(applyTimer)
  applyTimer = window.setInterval(async () => {
    if (!applyJob.value) return
    try {
      const res = await api.get<{ job: Job }>(`/jobs/${applyJob.value.id}`)
      applyJob.value = res.job
      if (applyDone.value) {
        window.clearInterval(applyTimer)
        const ok = res.job.done - res.job.failed
        if (res.job.status === 'succeeded') {
          toast(`已应用到 ${ok} 个实例`, 'success')
        } else {
          toast(`应用结束：成功 ${ok} 个，失败 ${res.job.failed} 个`, 'error')
        }
      }
    } catch {
      window.clearInterval(applyTimer)
    }
  }, 1000)
}

const applyProgress = computed(() => {
  if (!applyJob.value || !applyJob.value.total) return 0
  return Math.round((applyJob.value.done / applyJob.value.total) * 100)
})
const applyDone = computed(
  () => !!applyJob.value && ['succeeded', 'failed', 'partial'].includes(applyJob.value.status),
)
const applyRunning = computed(() => !!applyJob.value && !applyDone.value)

// --- unified config push ---
const cfgOpen = ref(false)
const cfgStarting = ref(false)
const cfgError = ref('')
const cfgJob = ref<Job | null>(null)
let cfgTimer = 0
onBeforeUnmount(() => window.clearInterval(cfgTimer))

const cfgDone = computed(
  () => !!cfgJob.value && ['succeeded', 'failed', 'partial'].includes(cfgJob.value.status),
)
const cfgRunning = computed(() => !!cfgJob.value && !cfgDone.value)
const cfgProgress = computed(() => {
  if (!cfgJob.value || !cfgJob.value.total) return 0
  return Math.round((cfgJob.value.done / cfgJob.value.total) * 100)
})

function openCfgApply() {
  if (cfgJob.value && !cfgDone.value) {
    cfgOpen.value = true
    return
  }
  cfgError.value = ''
  cfgJob.value = null
  cfgOpen.value = true
}

function closeCfgApply() {
  cfgOpen.value = false
}

async function startCfgApply() {
  cfgError.value = ''
  cfgStarting.value = true
  try {
    const res = await api.post<{ job: Job }>('/settings/apply-config')
    cfgJob.value = res.job
    cfgTimer = window.setInterval(async () => {
      if (!cfgJob.value) return
      try {
        const r = await api.get<{ job: Job }>(`/jobs/${cfgJob.value.id}`)
        cfgJob.value = r.job
        if (cfgDone.value) {
          window.clearInterval(cfgTimer)
          const ok = r.job.done - r.job.failed
          if (r.job.status === 'succeeded') {
            toast(`配置已下发到 ${ok} 个实例`, 'success')
          } else {
            toast(`下发结束：成功 ${ok} 个，失败 ${r.job.failed} 个`, 'error')
          }
        }
      } catch {
        window.clearInterval(cfgTimer)
      }
    }, 1000)
  } catch (e) {
    cfgError.value = errorMessage(e, '下发失败')
  } finally {
    cfgStarting.value = false
  }
}
const applySummary = computed(() => {
  if (!applyJob.value) return ''
  if (!applyDone.value) return '应用中'
  if (applyJob.value.status === 'succeeded') return '已完成'
  if (applyJob.value.status === 'partial') return '部分失败'
  return '失败'
})

function applyItemLabel(status: string) {
  switch (status) {
    case 'pending':
      return '排队中'
    case 'running':
      return '处理中'
    case 'succeeded':
      return '已应用'
    case 'failed':
      return '失败'
    default:
      return status
  }
}

function applyItemBadge(status: string) {
  if (status === 'succeeded') return 'badge-ok'
  if (status === 'failed') return 'badge-failed'
  return 'badge-neutral'
}

async function changePassword() {
  if (pwd.value.new_password !== pwd.value.confirm) {
    toast('两次输入的新密码不一致', 'error')
    return
  }
  pwdSaving.value = true
  try {
    await api.post('/auth/password', {
      current_password: pwd.value.current_password,
      new_password: pwd.value.new_password,
    })
    toast('密码已修改，请重新登录', 'success')
    auth.admin = null
    router.push('/login')
  } catch (e) {
    toast(errorMessage(e, '修改失败'), 'error')
  } finally {
    pwdSaving.value = false
  }
}
</script>

<template>
  <div v-if="settings" style="max-width: 1040px">
    <div class="grid grid-2">
      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">端口与账号</div>
            <div class="card-sub">改了只影响新租户，已有租户保持原端口</div>
          </div>
        </div>
        <div class="form-grid">
          <div class="field"><label>端口范围开始</label><input v-model.number="settings.port_start" class="input mono" type="number" /></div>
          <div class="field"><label>端口范围结束</label><input v-model.number="settings.port_end" class="input mono" type="number" /></div>
          <div class="field"><label>每个租户预留端口数</label><input v-model.number="settings.ports_per_tenant" class="input mono" type="number" min="1" max="10" /></div>
          <div class="field"><label>UID 范围开始</label><input v-model.number="settings.uid_start" class="input mono" type="number" /></div>
          <div class="field"><label>UID 范围结束</label><input v-model.number="settings.uid_end" class="input mono" type="number" /></div>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">默认资源上限</div>
            <div class="card-sub">新建租户时如果没单独填，就用这里的值</div>
          </div>
          <button
            class="btn btn-sm"
            type="button"
            :disabled="saving"
            @click="openApply"
          >
            {{ applyRunning ? `应用中 ${applyJob?.done}/${applyJob?.total}` : '应用到所有实例' }}
          </button>
        </div>
        <div class="form-grid">
          <div class="field"><label>CPU 上限</label><input v-model="settings.default_cpu_quota" class="input mono" placeholder="200%" /></div>
          <div class="field"><label>内存上限</label><input v-model="settings.default_memory_max" class="input mono" placeholder="4G" /></div>
          <div class="field"><label>进程数上限</label><input v-model.number="settings.default_tasks_max" class="input mono" type="number" min="1" /></div>
          <div class="field">
            <label>磁盘提醒阈值（GB，0=关闭）</label>
            <input v-model.number="settings.disk_warn_gb" class="input mono" type="number" min="0" />
          </div>
          <div class="field">
            <label>新租户默认磁盘上限（GB，0=不限制）</label>
            <input v-model.number="settings.default_disk_quota_gb" class="input mono" type="number" min="0" />
          </div>
          <div class="field">
            <label>用量历史保留天数</label>
            <input v-model.number="settings.metrics_retention_days" class="input mono" type="number" min="1" max="90" />
          </div>
        </div>
        <div class="small muted-2" style="margin-top: 10px">
          保存后只影响新租户；要让已有实例也用上，点右上角“应用到所有实例”。
        </div>
      </div>
    </div>

    <div class="grid grid-2" style="margin-top: 16px">
      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">备份</div>
            <div class="card-sub">只备份数据库和配置，不备份项目代码</div>
          </div>
        </div>
        <div class="stack">
          <label class="row small">
            <input v-model="settings.backup_enabled" class="checkbox" type="checkbox" />
            每天自动备份
          </label>
          <div class="form-grid">
            <div class="field"><label>备份时间（0-23 点）</label><input v-model.number="settings.backup_hour" class="input mono" type="number" min="0" max="23" /></div>
            <div class="field"><label>保留天数</label><input v-model.number="settings.backup_retention_days" class="input mono" type="number" min="1" /></div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">保留策略</div>
            <div class="card-sub">回收站和审计日志的清理周期</div>
          </div>
        </div>
        <div class="form-grid">
          <div class="field"><label>回收站保留天数</label><input v-model.number="settings.trash_retention_days" class="input mono" type="number" min="1" /></div>
          <div class="field"><label>审计保留天数</label><input v-model.number="settings.audit_retention_days" class="input mono" type="number" min="1" /></div>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">SSH 登录</div>
          <div class="card-sub">平台只准备租户账号，不会修改服务器的 sshd 配置</div>
        </div>
        <span class="badge" :class="sshReady ? 'badge-ok' : 'badge-failed'">
          {{ sshReady ? '可用' : '需要处理' }}
        </span>
      </div>
      <label class="row small">
        <input v-model="settings.ssh_enabled" class="checkbox" type="checkbox" />
        新建租户默认开启 SSH（每个租户还能单独关掉）
      </label>
      <div v-if="meta" class="detail-grid" style="margin-top: 12px">
        <div class="detail-item">
          <span class="detail-label">sshd</span>
          <span class="detail-value">{{ meta.ssh_status.installed ? (meta.ssh_status.running ? '已安装，运行中' : '已安装，未运行') : '未安装' }}</span>
        </div>
        <div class="detail-item">
          <span class="detail-label">端口</span>
          <span class="detail-value mono">{{ meta.ssh_status.port || 22 }}</span>
        </div>
        <div class="detail-item">
          <span class="detail-label">密码登录</span>
          <span class="detail-value">{{ meta.ssh_status.password_auth ? '已允许' : '未允许' }}</span>
        </div>
      </div>
      <div v-if="meta?.ssh_status.problem" class="row small" style="color: var(--amber); margin-top: 10px">
        {{ meta.ssh_status.problem }}
      </div>
      <div class="small muted-2" style="margin-top: 10px">
        员工用和网页登录相同的密码 SSH。要关闭密码登录的全局策略、或只允许密钥登录，请直接改服务器的
        sshd 配置，平台不会覆盖它。
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">对外访问</div>
          <div class="card-sub">员工拿到的地址会显示成这个主机名或 IP</div>
        </div>
      </div>
      <div class="form-grid">
        <div class="field">
          <label>对外主机名 / IP</label>
          <input v-model="settings.public_host" class="input mono" placeholder="留空则跟随浏览器的访问地址" />
        </div>
        <div class="field">
          <label>租户端口转发</label>
          <label class="row small" style="height: 38px">
            <input v-model="settings.allow_port_forward" class="checkbox" type="checkbox" />
            允许员工在自己的实例里开端口转发
          </label>
        </div>
      </div>
      <div class="small muted-2" style="margin-top: 10px">
        员工地址固定用 https。这里留空时，地址里的 IP 就用你现在浏览器访问平台所用的那个地址。
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">HTTPS 证书</div>
          <div class="card-sub">平台统一生成一份自签证书，下发给所有实例共用</div>
        </div>
        <div class="row">
          <label class="row small">
            <input v-model="settings.tls_enabled" class="checkbox" type="checkbox" />
            给所有实例启用 HTTPS
          </label>
          <button class="btn btn-sm" type="button" :disabled="certSaving" @click="regenerateCert">
            重新生成证书
          </button>
        </div>
      </div>
      <div class="form-grid">
        <div class="field">
          <label>证书目录（留空用数据目录下的 certs）</label>
          <input v-model="settings.tls_cert_dir" class="input mono" placeholder="留空则自动使用 &lt;数据目录&gt;/certs" />
        </div>
      </div>
      <div v-if="cert" class="detail-grid" style="margin-top: 12px">
        <div class="detail-item">
          <span class="detail-label">状态</span>
          <span class="detail-value">{{ cert.exists ? (cert.self_signed ? '自签证书，已就绪' : '证书已就绪') : '尚未生成' }}</span>
        </div>
        <div class="detail-item">
          <span class="detail-label">目录</span>
          <span class="detail-value mono small">{{ cert.dir }}</span>
        </div>
        <div class="detail-item">
          <span class="detail-label">有效期至</span>
          <span class="detail-value">{{ cert.not_after || '—' }}</span>
        </div>
        <div class="detail-item">
          <span class="detail-label">覆盖地址</span>
          <span class="detail-value mono small">{{ [...(cert.dns_names || []), ...(cert.ips || [])].join(', ') || '—' }}</span>
        </div>
      </div>
      <div class="small muted-2" style="margin-top: 10px">
        这是自签证书，员工第一次打开实例时浏览器会警告一次，点继续即可。换掉它或改完证书设置后，要下发一次才会生效。
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">统一配置下发</div>
          <div class="card-sub">这里填的配置项会覆盖实例里同名的设置，员工改不动</div>
        </div>
        <div class="row">
          <button class="btn btn-sm" type="button" @click="addConfigEntry">添加一项</button>
          <button class="btn btn-sm btn-primary" type="button" :disabled="cfgRunning" @click="openCfgApply">
            {{ cfgRunning ? `下发中 ${cfgJob?.done}/${cfgJob?.total}` : '下发到所有实例' }}
          </button>
        </div>
      </div>
      <table v-if="tenantConfig.length" class="table">
        <thead>
          <tr>
            <th style="width: 45%">配置项</th>
            <th>值</th>
            <th style="width: 60px"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(entry, i) in tenantConfig" :key="i">
            <td>
              <input v-model="entry.key" class="input mono" placeholder="例如 chat.page_size" />
            </td>
            <td>
              <input v-model="entry.value" class="input mono" placeholder="值" />
            </td>
            <td>
              <button class="btn btn-ghost btn-sm" type="button" @click="removeConfigEntry(i)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="small muted-2">还没有配置项。点「添加一项」，比如 <span class="mono">chat.page_size</span> = <span class="mono">50</span>。</div>
      <div class="small muted-2" style="margin-top: 10px">
        端口、监听地址、密码、证书目录由平台自己维护，不能在这里覆盖。保存只存平台这边，点「下发到所有实例」才会写进实例并重启它们。
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">员工自助页</div>
          <div class="card-sub">免登录的只读页面，员工在上面查自己的实例地址和端口</div>
        </div>
        <a class="btn btn-sm" :href="`/guest`" target="_blank" rel="noopener">打开看看</a>
      </div>
      <div class="stack">
        <label class="row small">
          <input v-model="settings.guest_enabled" class="checkbox" type="checkbox" />
          开启员工自助页（任何能访问本机的人都能看到实例名和端口，不需要登录）
        </label>
        <div class="field">
          <label>公告（显示在页面顶部，可留空）</label>
          <textarea
            v-model="settings.guest_notice"
            class="textarea"
            maxlength="500"
            placeholder="例如：本周六 9:00-12:00 例行维护，期间实例可能短暂中断"
          />
        </div>
        <div class="field">
          <label>使用说明（显示在页面底部，可留空则用默认文案）</label>
          <textarea
            v-model="settings.guest_help"
            class="textarea"
            maxlength="2000"
            placeholder="告诉员工怎么用、找谁帮忙"
          />
        </div>
        <div class="small muted-2">
          页面地址：<a class="mono" :href="guestURL" target="_blank" rel="noopener">{{ guestURL }}</a>
        </div>
      </div>
    </div>

    <div class="form-actions" style="justify-content: flex-start">
      <button class="btn btn-primary" type="button" :disabled="saving" @click="save">
        <Save :size="15" /> {{ saving ? '保存中…' : '保存设置' }}
      </button>
      <button class="btn" type="button" @click="load">重新加载</button>
    </div>

    <div class="grid grid-2" style="margin-top: 16px">
      <div class="card">
        <div class="card-header">
          <div class="card-title"><KeyRound :size="15" style="vertical-align: -2px" /> 修改管理员密码</div>
        </div>
        <div class="stack">
          <div class="field"><label>当前密码</label><input v-model="pwd.current_password" class="input" type="password" autocomplete="current-password" /></div>
          <div class="field"><label>新密码</label><input v-model="pwd.new_password" class="input" type="password" autocomplete="new-password" /></div>
          <div class="field"><label>再输一次新密码</label><input v-model="pwd.confirm" class="input" type="password" autocomplete="new-password" /></div>
        </div>
        <div class="form-actions" style="justify-content: flex-start">
          <button class="btn" type="button" :disabled="pwdSaving" @click="changePassword">
            {{ pwdSaving ? '修改中…' : '修改密码' }}
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title"><Server :size="15" style="vertical-align: -2px" /> 平台信息</div>
        </div>
        <div class="detail-grid">
          <div class="detail-item"><span class="detail-label">运行模式</span><span class="detail-value mono">{{ meta?.mode }}</span></div>
          <div class="detail-item"><span class="detail-label">平台版本</span><span class="detail-value mono">{{ meta?.fleet_version }}</span></div>
          <div class="detail-item"><span class="detail-label">当前 Clawbench 版本</span><span class="detail-value mono">{{ meta?.current_version || '未上传' }}</span></div>
          <div class="detail-item"><span class="detail-label">对外地址</span><span class="detail-value mono">{{ meta?.public_host }}</span></div>
          <div class="detail-item" style="grid-column: span 2"><span class="detail-label">安装目录</span><span class="detail-value mono">{{ meta?.install_dir }}</span></div>
        </div>
        <div class="row small muted-2" style="margin-top: 12px">
          <ShieldCheck :size="14" />
          <span>管理员密码只存在平台数据库里，使用 bcrypt 哈希保存。</span>
        </div>
      </div>
    </div>

    <Modal v-if="applyOpen" :title="applyJob ? '应用到所有实例' : '把默认资源上限应用到所有实例'" wide @close="closeApply">
      <template v-if="!applyJob">
        <p class="muted small" style="margin-top: 0">
          平台会先保存上面的设置，再把下面这组默认值推送给所有实例（回收站里的除外）。
        </p>
        <div class="detail-grid" style="margin-bottom: 12px">
          <div class="detail-item"><span class="detail-label">CPU 上限</span><span class="detail-value mono">{{ settings?.default_cpu_quota }}</span></div>
          <div class="detail-item"><span class="detail-label">内存上限</span><span class="detail-value mono">{{ settings?.default_memory_max }}</span></div>
          <div class="detail-item"><span class="detail-label">进程数上限</span><span class="detail-value mono">{{ settings?.default_tasks_max }}</span></div>
        </div>
        <ul class="small muted" style="margin: 0; padding-left: 18px; line-height: 1.9">
          <li>已经单独设置过配额的实例也会被覆盖。</li>
          <li>运行中的实例会重启一次，短暂中断几秒。</li>
          <li>已停止或启动失败的实例只改配置，下次启动生效。</li>
          <li>正在创建或删除的实例会被跳过。</li>
        </ul>
        <div v-if="applyError" class="small" style="color: var(--red); margin-top: 10px">{{ applyError }}</div>
      </template>
      <template v-else>
        <div class="row-between" style="margin-bottom: 10px; flex-wrap: wrap; gap: 8px">
          <span class="small muted-2">
            {{ applyJob.done }} / {{ applyJob.total }}
            <span v-if="applyJob.failed" style="color: var(--red)"> · 失败 {{ applyJob.failed }}</span>
          </span>
          <span
            class="badge"
            :class="applyDone ? (applyJob.status === 'succeeded' ? 'badge-ok' : applyJob.status === 'partial' ? 'badge-neutral' : 'badge-failed') : 'badge-neutral'"
          >
            {{ applySummary }}
          </span>
        </div>
        <div class="progress" style="margin-bottom: 14px"><span :style="{ width: applyProgress + '%' }" /></div>
        <div v-if="!(applyJob.items || []).length" class="muted small" style="padding: 12px 0">正在准备任务…</div>
        <div v-else class="table-wrap" style="max-height: 46vh; overflow: auto">
          <table class="table">
            <thead>
              <tr><th>实例</th><th>状态</th><th>说明</th></tr>
            </thead>
            <tbody>
              <tr v-for="item in applyJob.items || []" :key="item.id">
                <td class="mono small">{{ item.name }}</td>
                <td>
                  <span class="badge" :class="applyItemBadge(item.status)">{{ applyItemLabel(item.status) }}</span>
                </td>
                <td class="small" :style="item.error ? 'color: var(--red)' : ''">{{ item.error || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="applyRunning" class="small muted-2" style="margin-top: 10px">
          可以关掉这个窗口，任务会在后台继续，完成后会提示。
        </div>
      </template>
      <template #footer>
        <template v-if="!applyJob">
          <button class="btn" type="button" @click="closeApply">取消</button>
          <button class="btn btn-primary" type="button" :disabled="applyStarting" @click="startApply">
            {{ applyStarting ? '提交中…' : '确认应用' }}
          </button>
        </template>
        <template v-else>
          <button v-if="applyRunning" class="btn" type="button" @click="closeApply">后台继续，关闭</button>
          <button v-else class="btn btn-primary" type="button" @click="closeApply">关闭</button>
        </template>
      </template>
    </Modal>

    <Modal v-if="cfgOpen" :title="cfgJob ? '下发统一配置' : '把统一配置下发到所有实例'" wide @close="closeCfgApply">
      <template v-if="!cfgJob">
        <p class="muted small" style="margin-top: 0">
          平台会把下面这组配置项写进每个实例的 config.yaml（回收站里的除外），同名项以这里的值为准。
        </p>
        <div v-if="!tenantConfig.length" class="small muted-2" style="margin-bottom: 12px">
          现在还没有配置项，下发只会重写端口、证书目录这些平台自己的项。
        </div>
        <div v-else class="table-wrap" style="max-height: 30vh; overflow: auto; margin-bottom: 12px">
          <table class="table">
            <thead>
              <tr><th>配置项</th><th>值</th></tr>
            </thead>
            <tbody>
              <tr v-for="(entry, i) in tenantConfig" :key="i">
                <td class="mono small">{{ entry.key }}</td>
                <td class="mono small">{{ entry.value }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <ul class="small muted" style="margin: 0; padding-left: 18px; line-height: 1.9">
          <li>运行中的实例会重启一次，短暂中断几秒。</li>
          <li>已停止或启动失败的实例只改配置文件，下次启动生效。</li>
          <li>正在创建或删除的实例会被跳过。</li>
          <li>改完配置项记得先点「保存设置」，再点这里下发。</li>
        </ul>
        <div v-if="cfgError" class="small" style="color: var(--red); margin-top: 10px">{{ cfgError }}</div>
      </template>
      <template v-else>
        <div class="row-between" style="margin-bottom: 10px; flex-wrap: wrap; gap: 8px">
          <span class="small muted-2">
            {{ cfgJob.done }} / {{ cfgJob.total }}
            <span v-if="cfgJob.failed" style="color: var(--red)"> · 失败 {{ cfgJob.failed }}</span>
          </span>
          <span
            class="badge"
            :class="cfgDone ? (cfgJob.status === 'succeeded' ? 'badge-ok' : cfgJob.status === 'partial' ? 'badge-neutral' : 'badge-failed') : 'badge-neutral'"
          >
            {{ cfgDone ? (cfgJob.status === 'succeeded' ? '已完成' : cfgJob.status === 'partial' ? '部分失败' : '失败') : '下发中' }}
          </span>
        </div>
        <div class="progress" style="margin-bottom: 14px"><span :style="{ width: cfgProgress + '%' }" /></div>
        <div v-if="!(cfgJob.items || []).length" class="muted small" style="padding: 12px 0">正在准备任务…</div>
        <div v-else class="table-wrap" style="max-height: 46vh; overflow: auto">
          <table class="table">
            <thead>
              <tr><th>实例</th><th>状态</th><th>说明</th></tr>
            </thead>
            <tbody>
              <tr v-for="item in cfgJob.items || []" :key="item.id">
                <td class="mono small">{{ item.name }}</td>
                <td><span class="badge" :class="applyItemBadge(item.status)">{{ applyItemLabel(item.status) }}</span></td>
                <td class="small" :style="item.error ? 'color: var(--red)' : ''">{{ item.error || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="cfgRunning" class="small muted-2" style="margin-top: 10px">
          可以关掉这个窗口，任务会在后台继续，完成后会提示。
        </div>
      </template>
      <template #footer>
        <template v-if="!cfgJob">
          <button class="btn" type="button" @click="closeCfgApply">取消</button>
          <button class="btn btn-primary" type="button" :disabled="cfgStarting" @click="startCfgApply">
            {{ cfgStarting ? '提交中…' : '确认下发' }}
          </button>
        </template>
        <template v-else>
          <button v-if="cfgRunning" class="btn" type="button" @click="closeCfgApply">后台继续，关闭</button>
          <button v-else class="btn btn-primary" type="button" @click="closeCfgApply">关闭</button>
        </template>
      </template>
    </Modal>
  </div>
</template>
