<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertTriangle, ArrowUpCircle, Download, Play, RotateCcw, UploadCloud } from 'lucide-vue-next'
import { api } from '../api'
import ConfirmDialog from '../components/ConfirmDialog.vue'
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
const npmVersions = ref<NpmVersion[]>([])
const npmWarning = ref('')
const npmChoice = ref('')
const npmLoading = ref(false)
const installing = ref(false)
let timer = 0

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
  } catch (e: any) {
    toast(e?.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
  loadNpm()
}

async function loadNpm() {
  npmLoading.value = true
  try {
    const res = await api.get<{ package: string; versions: NpmVersion[]; warning: string }>('/upgrades/npm')
    npmPackage.value = res.package || ''
    npmVersions.value = res.versions || []
    npmWarning.value = res.warning || ''
    if (!npmChoice.value || !npmVersions.value.some((v) => v.version === npmChoice.value)) {
      const firstNew = npmVersions.value.find((v) => !v.installed)
      npmChoice.value = (firstNew || npmVersions.value[0])?.version || ''
    }
  } catch (e: any) {
    npmWarning.value = e?.message || '读取 npm 版本列表失败'
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
  } catch (e: any) {
    toast(e?.message || '安装失败', 'error')
  } finally {
    installing.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => window.clearInterval(timer))

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
  } catch (e: any) {
    toast(e?.message || '上传失败', 'error')
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
  } catch (e: any) {
    toast(e?.message || '试点失败，已自动回滚', 'error')
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
  } catch (e: any) {
    toast(e?.message || '批量升级失败', 'error')
  }
}

async function doRollback() {
  confirmRollback.value = false
  try {
    const res = await api.post<{ job: Job }>('/upgrades/rollback')
    activeJob.value = res.job
    toast('回滚已开始', 'success')
    pollJob(res.job.id)
  } catch (e: any) {
    toast(e?.message || '回滚失败', 'error')
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
              {{ npmPackage ? `直接从 ${npmPackage} 拉取，装进独立目录，旧版本保留可回滚` : '从 npm 拉取新版本' }}
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
        <div v-else-if="!npmLoading && !npmWarning" class="empty" style="padding: 14px">npm 上没有查到可用版本。</div>
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
      <div v-else class="empty" style="padding: 22px">还没有上传任何版本。</div>
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
