<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertTriangle, ArrowUpCircle, Download, Play, RotateCcw, UploadCloud } from 'lucide-vue-next'
import { api, errorMessage } from '../api'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import LoadingState from '../components/LoadingState.vue'
import { toast } from '../stores/toast'
import type { Job, NpmVersion, Tenant, Version } from '../types'
import { formatBytes, formatTime, shortSha } from '../utils'

const versions = ref<Version[]>([])
const current = ref<Version | null>(null)
const jobs = ref<Job[]>([])
const running = ref<Tenant[]>([])
const loading = ref(false)
const uploading = ref(false)
const file = ref<File | null>(null)
const pilotTenant = ref(0)
const activeJob = ref<Job | null>(null)
const pendingApply = ref<Version | null>(null)
const confirmRollback = ref(false)
const npmPackage = ref('')
const npmRegistry = ref('')
const npmRegistryFrom = ref('')
const npmVersions = ref<NpmVersion[]>([])
const npmWarning = ref('')
const npmChoice = ref('')
const npmLoading = ref(false)
const installing = ref(false)
const selfInfo = ref<{
  version: string
  binary: string
  supported: boolean
  mode: string
  adopted: boolean
} | null>(null)
const selfFile = ref<File | null>(null)
const selfUploading = ref(false)
const selfWaiting = ref(false)
const selfWarning = ref('')
const confirmSelf = ref(false)
const adopting = ref(false)
const confirmAdopt = ref(false)
let timer = 0
let selfTimer = 0

async function load() {
  loading.value = true
  try {
    const [upgrades, tenants] = await Promise.all([
      api.get<{ versions: Version[]; current: Version | null; jobs: Job[] }>('/upgrades'),
      api.get<{ items: Tenant[] }>('/tenants?status=running&page_size=200'),
    ])
    versions.value = upgrades.versions || []
    current.value = upgrades.current
    jobs.value = upgrades.jobs || []
    running.value = tenants.items || []
    if (!pilotTenant.value && running.value.length) {
      pilotTenant.value = running.value[0].id
    }
  } catch (e) {
    toast(errorMessage(e, '加载失败'), 'error')
  } finally {
    loading.value = false
  }
  loadNpm()
  loadSelf()
}

async function loadSelf() {
  try {
    selfInfo.value = await api.get<{
      version: string
      binary: string
      supported: boolean
      mode: string
      adopted: boolean
    }>('/upgrades/self')
  } catch (e) {
    selfWarning.value = errorMessage(e, '读取平台版本失败')
  }
}

function chooseSelfFile(event: Event) {
  const f = (event.target as HTMLInputElement).files?.[0]
  if (f) selfFile.value = f
}

// waitForSelf 在平台重启期间反复问 /api/health，直到新进程应答为止。
// 上传请求本身会在进程退出时断开，所以断开之后的完成状态只能靠这里确认。
function waitForSelf(expected: string, action: 'upgrade' | 'adopt' = 'upgrade') {
  selfWaiting.value = true
  const deadline = Date.now() + 3 * 60 * 1000
  window.clearInterval(selfTimer)
  selfTimer = window.setInterval(async () => {
    try {
      const res = await fetch('/api/health', { credentials: 'include' })
      const data = (await res.json()) as { version?: string }
      if (res.ok) {
        window.clearInterval(selfTimer)
        selfWaiting.value = false
        const got = data.version || ''
        if (got.replace(/^v/, '') === expected.replace(/^v/, '')) {
          toast(action === 'adopt'
            ? `平台已由系统托管并重新上线（版本 ${got}）`
            : `平台已升级到 ${got}，正在刷新页面`, 'success')
          window.setTimeout(() => window.location.reload(), 1200)
        } else {
          // 版本对不上，说明系统拉起来的可能是另一份程序。自动刷新会把这条
          // 提示冲掉，所以留在页面上把话说清楚。
          selfWarning.value = `平台已重新上线，但报告的版本是 ${got || '未知'}，期望 ${expected}。请核对 systemd 的 ExecStart 是否指向这次运行的程序。`
          toast(selfWarning.value, 'error')
        }
      }
    } catch {
      // 服务还没起来，继续等。
      if (Date.now() > deadline) {
        window.clearInterval(selfTimer)
        selfWaiting.value = false
        toast('平台重启后一直没响应，请到服务器上检查 systemctl status clawbench-fleet', 'error')
      }
    }
  }, 2000)
}

async function uploadSelf() {
  if (!selfFile.value) return
  confirmSelf.value = false
  selfUploading.value = true
  selfWarning.value = ''
  try {
    const res = await api.upload<{
      result: { uploaded_version: string; backup_path: string; restarting: boolean }
      restarting: boolean
      warning?: string
    }>('/upgrades/self', selfFile.value)
    if (res.warning) selfWarning.value = res.warning
    if (res.result.backup_path) {
      toast(`旧程序已备份到 ${res.result.backup_path}`, 'success')
    }
    selfFile.value = null
    selfUploading.value = false
    waitForSelf(res.result.uploaded_version)
  } catch (e) {
    selfUploading.value = false
    toast(errorMessage(e, '上传失败，平台没有改动'), 'error')
  }
}

async function adoptSelf() {
  confirmAdopt.value = false
  adopting.value = true
  selfWarning.value = ''
  try {
    const res = await api.post<{
      result: { service_path: string; config_path: string; stopped: number; restarting: boolean }
      restarting: boolean
      warning?: string
    }>('/upgrades/self/adopt')
    if (res.warning) selfWarning.value = res.warning
    toast(res.result.stopped > 0
      ? `服务定义已写入 ${res.result.service_path}，停掉了 ${res.result.stopped} 个手工启动的进程`
      : `服务定义已写入 ${res.result.service_path}`, 'success')
    adopting.value = false
    waitForSelf(selfInfo.value?.version || '', 'adopt')
  } catch (e) {
    adopting.value = false
    toast(errorMessage(e, '交给 systemd 托管失败'), 'error')
  }
}

async function loadNpm() {
  npmLoading.value = true
  try {
    const res = await api.get<{
      package: string
      registry: string
      registry_from: string
      versions: NpmVersion[]
      warning: string
    }>('/upgrades/npm')
    npmPackage.value = res.package || ''
    npmRegistry.value = res.registry || ''
    npmRegistryFrom.value = res.registry_from || ''
    npmVersions.value = res.versions || []
    npmWarning.value = res.warning || ''
    if (!npmChoice.value || !npmVersions.value.some((v) => v.version === npmChoice.value)) {
      const firstNew = npmVersions.value.find((v) => !v.installed)
      npmChoice.value = (firstNew || npmVersions.value[0])?.version || ''
    }
  } catch (e) {
    npmWarning.value = errorMessage(e, '读取 npm 版本列表失败')
    npmVersions.value = []
  } finally {
    npmLoading.value = false
  }
}

async function installNpm() {
  if (!npmChoice.value) return
  const version = npmChoice.value
  installing.value = true
  try {
    await api.post<{ version: Version }>(`/upgrades/npm/${encodeURIComponent(version)}/install`)
    toast(`已从 npm 安装 ${version}`, 'success')
    await load()
  } catch (e) {
    toast(errorMessage(e, '安装失败'), 'error')
  } finally {
    installing.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => {
  window.clearInterval(timer)
  window.clearInterval(selfTimer)
})

function chooseFile(event: Event) {
  const f = (event.target as HTMLInputElement).files?.[0]
  if (f) file.value = f
}

async function upload() {
  if (!file.value) return
  uploading.value = true
  try {
    await api.upload<{ version: Version }>('/upgrades/upload', file.value)
    toast('版本上传成功', 'success')
    file.value = null
    load()
  } catch (e) {
    toast(errorMessage(e, '上传失败'), 'error')
  } finally {
    uploading.value = false
  }
}

async function pilot(version: Version) {
  if (!pilotTenant.value) {
    toast('请先选择一个运行中的试点租户', 'error')
    return
  }
  try {
    await api.post(`/upgrades/${encodeURIComponent(version.version)}/pilot`, { tenant_id: pilotTenant.value })
    toast(`试点成功：${version.version}`, 'success')
    load()
  } catch (e) {
    toast(errorMessage(e, '试点失败，已自动回滚'), 'error')
    load()
  }
}

async function doApply() {
  if (!pendingApply.value) return
  const version = pendingApply.value
  pendingApply.value = null
  try {
    const res = await api.post<{ job: Job }>(`/upgrades/${encodeURIComponent(version.version)}/apply`)
    activeJob.value = res.job
    toast('批量升级已开始', 'success')
    pollJob(res.job.id)
  } catch (e) {
    toast(errorMessage(e, '批量升级失败'), 'error')
  }
}

async function doRollback() {
  confirmRollback.value = false
  try {
    const res = await api.post<{ job: Job }>('/upgrades/rollback')
    activeJob.value = res.job
    toast('回滚已开始', 'success')
    pollJob(res.job.id)
  } catch (e) {
    toast(errorMessage(e, '回滚失败'), 'error')
  }
}

function pollJob(id: number) {
  window.clearInterval(timer)
  timer = window.setInterval(async () => {
    try {
      const res = await api.get<{ job: Job }>(`/jobs/${id}`)
      activeJob.value = res.job
      if (['succeeded', 'failed', 'partial'].includes(res.job.status)) {
        window.clearInterval(timer)
        if (res.job.status === 'succeeded') {
          toast('升级任务完成', 'success')
        } else {
          toast('升级失败，已经回滚到上一版', 'error')
        }
        load()
      }
    } catch {
      window.clearInterval(timer)
    }
  }, 1000)
}

const progress = computed(() => {
  if (!activeJob.value || !activeJob.value.total) return 0
  return Math.round((activeJob.value.done / activeJob.value.total) * 100)
})

function statusBadge(v: Version): string {
  if (v.status === 'current') return 'badge-ok'
  if (v.status === 'previous') return 'badge-neutral'
  return 'badge-neutral'
}

function statusText(v: Version): string {
  if (v.status === 'current') return '当前版本'
  if (v.status === 'previous') return '上一版'
  return '已安装'
}
</script>

<template>
  <div>
    <div class="grid grid-2">
      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">当前版本</div>
            <div class="card-sub">所有实例共享同一份程序，升级就是切软链再重启</div>
          </div>
          <ArrowUpCircle :size="20" color="var(--accent)" />
        </div>
        <div v-if="current">
          <div class="stat-value mono" style="font-size: 24px">{{ current.version }}</div>
          <div class="small muted-2" style="margin-top: 4px">
            安装于 {{ formatTime(current.uploaded_at) }} · {{ formatBytes(current.size_bytes) }}
          </div>
          <div class="small mono muted-2" style="margin-top: 2px">SHA-256 {{ shortSha(current.sha256) }}</div>
        </div>
        <div v-else-if="loading" class="empty" style="padding: 18px">正在读取版本信息…</div>
        <div v-else class="empty" style="padding: 18px">还没有当前版本，先从 npm 安装或上传一个 Clawbench 二进制。</div>
        <div class="form-actions" style="justify-content: flex-start">
          <button class="btn" type="button" :disabled="!current" @click="confirmRollback = true">
            <RotateCcw :size="15" /> 回滚到上一版
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <div class="card-title">从 npm 安装</div>
            <div class="card-sub">
              {{ npmPackage ? `从 ${npmPackage} 拉取，装进独立目录，旧版本保留可回滚` : '从 npm 拉取新版本' }}
            </div>
            <div v-if="npmRegistry" class="small mono muted-2" style="margin-top: 5px; word-break: break-all">
              源：{{ npmRegistry }}
              <span class="badge badge-neutral" style="margin-left: 6px">
                {{ npmRegistryFrom === 'config' ? '配置文件指定' : '服务器默认' }}
              </span>
            </div>
          </div>
          <Download :size="20" color="var(--accent)" />
        </div>
        <div v-if="npmWarning" class="row small" style="color: var(--amber); align-items: flex-start; margin-bottom: 10px">
          <AlertTriangle :size="15" style="flex: 0 0 auto; margin-top: 1px" />
          <span>{{ npmWarning }}<br />可以用下面的“离线安装”上传安装包。</span>
        </div>
        <div class="field" v-if="npmVersions.length">
          <label>可用版本</label>
          <select v-model="npmChoice" class="select mono">
            <option v-for="v in npmVersions" :key="v.version" :value="v.version">
              {{ v.version }}{{ v.installed ? '（已安装）' : '' }}
            </option>
          </select>
        </div>
        <div v-else-if="npmLoading" class="empty" style="padding: 14px">正在查询 npm 上的版本…</div>
        <div v-else-if="!npmWarning" class="empty" style="padding: 14px">npm 上没有查到可用版本。</div>
        <div class="form-actions" style="justify-content: flex-start">
          <button
            class="btn btn-primary"
            type="button"
            :disabled="!npmChoice || installing || npmLoading"
            @click="installNpm"
          >
            {{ installing ? '下载并校验中…' : npmLoading ? '查询中…' : '安装到本机' }}
          </button>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">已安装版本</div>
          <div class="card-sub">试点会先切软链、只重启选中的实例；失败会自动切回去</div>
        </div>
        <div class="row">
          <select v-model.number="pilotTenant" class="select" style="width: 190px">
            <option :value="0" disabled>选择试点租户</option>
            <option v-for="t in running" :key="t.id" :value="t.id">{{ t.username }}（:{{ t.port }}）</option>
          </select>
        </div>
      </div>

      <div v-if="versions.length" class="table-wrap">
        <table class="table">
          <thead><tr><th>版本</th><th>状态</th><th>大小</th><th>安装时间</th><th>SHA-256</th><th style="text-align: right">操作</th></tr></thead>
          <tbody>
            <tr v-for="v in versions" :key="v.id">
              <td class="mono">{{ v.version }}</td>
              <td><span class="badge" :class="statusBadge(v)">{{ statusText(v) }}</span></td>
              <td class="small num">{{ formatBytes(v.size_bytes) }}</td>
              <td class="small">{{ formatTime(v.uploaded_at) }}</td>
              <td class="small mono muted-2">{{ shortSha(v.sha256) }}</td>
              <td>
                <div class="actions">
                  <button class="btn btn-sm" type="button" :disabled="!running.length" @click="pilot(v)">
                    <Play :size="13" /> 试点
                  </button>
                  <button
                    class="btn btn-sm btn-primary"
                    type="button"
                    :disabled="!running.length"
                    @click="pendingApply = v"
                  >
                    批量升级
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else-if="loading">
        <LoadingState text="正在读取已安装版本…" />
      </div>
      <EmptyState v-else title="还没有上传任何版本" hint="从上面「从 npm 安装」或下面的「离线安装」来装第一个版本。" />
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">离线安装（上传文件）</div>
          <div class="card-sub">服务器连不上 npm 时用这个；上传后先拿一个实例试点，通过再批量</div>
        </div>
        <UploadCloud :size="20" color="var(--muted)" />
      </div>
      <label class="upload-box">
        <input type="file" style="display: none" @change="chooseFile" />
        <UploadCloud :size="22" />
        <div class="small" style="margin-top: 6px">{{ file ? file.name : '选择 Clawbench 二进制文件' }}</div>
        <div class="small muted-2">{{ file ? formatBytes(file.size) : '最大 600 MB' }}</div>
      </label>
      <div class="form-actions" style="justify-content: flex-start">
        <button class="btn" type="button" :disabled="!file || uploading" @click="upload">
          {{ uploading ? '上传并校验中…' : '上传版本' }}
        </button>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">升级平台自身</div>
          <div class="card-sub">
            上传一个 clawbench-fleet 二进制，替换本平台程序并自动重启服务；只影响管理后台，不动任何实例
          </div>
        </div>
        <ArrowUpCircle :size="20" color="var(--accent)" />
      </div>
      <div class="small muted-2" style="margin-bottom: 10px">
        当前平台版本 <span class="mono">{{ selfInfo?.version || '—' }}</span>
        <span v-if="selfInfo?.binary" class="mono" style="word-break: break-all"> · {{ selfInfo.binary }}</span>
      </div>
      <div v-if="selfInfo && selfInfo.supported && !selfInfo.adopted" class="row small" style="color: var(--amber); align-items: flex-start; margin-bottom: 10px">
        <AlertTriangle :size="15" style="flex: 0 0 auto; margin-top: 1px" />
        <span>平台还没交给系统托管，现在升级会在替换程序后无法重新启动。请先点下面的「交给系统托管并重启」，成功之后再回来升级。</span>
      </div>
      <div v-if="selfInfo && !selfInfo.supported" class="row small" style="color: var(--amber); align-items: flex-start; margin-bottom: 10px">
        <AlertTriangle :size="15" style="flex: 0 0 auto; margin-top: 1px" />
        <span>当前是开发模式，不提供平台自升级。请在正式部署（systemd）的服务器上使用。</span>
      </div>
      <div v-if="selfWarning" class="row small" style="color: var(--amber); align-items: flex-start; margin-bottom: 10px">
        <AlertTriangle :size="15" style="flex: 0 0 auto; margin-top: 1px" />
        <span>{{ selfWarning }}</span>
      </div>
      <div v-if="selfWaiting" class="row small" style="color: var(--accent); align-items: center">
        <span>新版本已写入，正在等待平台重启…页面会在平台回来后自动刷新。</span>
      </div>
      <template v-else>
        <label class="upload-box">
          <input type="file" style="display: none" :disabled="!selfInfo?.supported || !selfInfo?.adopted" @change="chooseSelfFile" />
          <UploadCloud :size="22" />
          <div class="small" style="margin-top: 6px">{{ selfFile ? selfFile.name : '选择 clawbench-fleet 二进制文件' }}</div>
          <div class="small muted-2">{{ selfFile ? formatBytes(selfFile.size) : '最大 600 MB' }}</div>
        </label>
        <div class="form-actions" style="justify-content: flex-start">
          <button
            class="btn"
            type="button"
            :disabled="!selfFile || selfUploading || !selfInfo?.supported || !selfInfo?.adopted"
            @click="confirmSelf = true"
          >
            {{ selfUploading ? '上传并校验中…' : '上传并重启平台' }}
          </button>
        </div>
        <div class="row small muted-2" style="align-items: flex-start; margin-top: 12px">
          <span>
            如果平台是用 nohup 之类手工挂起来的，升级后不会有人把它重新拉起来，页面会一直打不开。点下面的按钮把当前程序交给系统托管：
            写服务定义、设成开机自启，然后停掉手工启动的进程并重新启动。
          </span>
        </div>
        <div class="form-actions" style="justify-content: flex-start; margin-top: 8px">
          <span v-if="selfInfo?.adopted" class="badge badge-ok" style="align-self: center">
            已由系统托管
          </span>
          <span v-if="selfInfo?.adopted" class="small muted-2" style="align-self: center">
            服务 clawbench-fleet.service 由 systemd 管理，开机会自动启动，不需要再点这里。
          </span>
          <button
            v-else
            class="btn"
            type="button"
            :disabled="adopting || !selfInfo?.supported"
            @click="confirmAdopt = true"
          >
            {{ adopting ? '正在托管中…' : '交给系统托管并重启' }}
          </button>
        </div>
      </template>
    </div>

    <div v-if="activeJob" class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">任务进度</div>
          <div class="card-sub">{{ activeJob.type }} · {{ activeJob.done }} / {{ activeJob.total }}，失败 {{ activeJob.failed }}</div>
        </div>
        <span class="badge" :class="activeJob.status === 'succeeded' ? 'badge-ok' : activeJob.status === 'failed' ? 'badge-failed' : 'badge-neutral'">
          {{ activeJob.status }}
        </span>
      </div>
      <div class="progress" style="margin-bottom: 14px"><span :style="{ width: progress + '%' }" /></div>
      <div v-if="activeJob.items?.length" class="table-wrap" style="max-height: 360px; overflow: auto">
        <table class="table">
          <thead><tr><th>租户</th><th>状态</th><th>错误</th></tr></thead>
          <tbody>
            <tr v-for="item in activeJob.items" :key="item.id">
              <td class="mono small">{{ item.name }}</td>
              <td>
                <span class="badge" :class="item.status === 'succeeded' ? 'badge-ok' : item.status === 'failed' ? 'badge-failed' : 'badge-neutral'">
                  {{ item.status }}
                </span>
              </td>
              <td class="small">{{ item.error || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="small muted-2">正在准备这个任务，稍等一下…</div>
    </div>

    <div class="card" style="margin-top: 16px; border-color: rgba(245,185,76,.3)">
      <div class="row small" style="color: var(--amber); align-items: flex-start">
        <AlertTriangle :size="16" style="flex: 0 0 auto; margin-top: 1px" />
        <span>
          批量升级会逐个重启运行中的实例，员工会短暂断线。Clawbench 有客户端版本检查，升级服务端后旧客户端可能被要求更新。
        </span>
      </div>
    </div>

    <ConfirmDialog
      :open="!!pendingApply"
      title="批量升级"
      :message="`把所有运行中的实例升级到 ${pendingApply?.version || ''}。\n\n每个实例会重启一次，员工会短暂断线。任何一个失败都会立即停止并回滚已升级的实例。`"
      confirm-text="开始批量升级"
      danger
      @cancel="pendingApply = null"
      @confirm="doApply"
    />

    <ConfirmDialog
      :open="confirmSelf"
      title="升级平台自身"
      message="会用上传的文件替换正在运行的管理后台程序，然后重启 clawbench-fleet 服务。\n\n期间后台页面会断开几十秒，员工实例完全不受影响。旧程序会备份在同目录的 .bak 文件里，出问题可以手工换回去。"
      confirm-text="确认替换并重启"
      danger
      @cancel="confirmSelf = false"
      @confirm="uploadSelf"
    />

    <ConfirmDialog
      :open="confirmAdopt"
      title="交给系统托管"
      message="会写一份 clawbench-fleet.service 服务定义（程序路径取当前正在运行的这个程序，配置文件沿用启动时的 --config），设成开机自启，然后停掉手工启动的进程并由系统重新启动平台。\n\n期间后台页面会断开几十秒，员工实例完全不受影响。只停掉占用本平台端口的那个进程，不会碰其他程序。"
      confirm-text="确认托管并重启"
      danger
      @cancel="confirmAdopt = false"
      @confirm="adoptSelf"
    />

    <ConfirmDialog
      :open="confirmRollback"
      title="回滚版本"
      message="把所有运行中的实例切回上一版并重启。回滚本身也会逐个做健康检查，失败会停止并报告。"
      confirm-text="确认回滚"
      danger
      @cancel="confirmRollback = false"
      @confirm="doRollback"
    />
  </div>
</template>

<style scoped>
.upload-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 118px;
  border: 1px dashed var(--border);
  border-radius: var(--radius);
  background: rgba(9, 15, 28, 0.4);
  cursor: pointer;
  text-align: center;
}
.upload-box:hover {
  border-color: rgba(91, 140, 255, 0.55);
}
</style>
