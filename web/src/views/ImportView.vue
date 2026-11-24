<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { AlertTriangle, CheckCircle2, Download, FileSpreadsheet, UploadCloud } from 'lucide-vue-next'
import { api, downloadFile, errorMessage } from '../api'
import { toast } from '../stores/toast'
import type { ImportRow, Job } from '../types'

const file = ref<File | null>(null)
const preview = ref<ImportRow[]>([])
const total = ref(0)
const importable = ref(0)
const invalid = ref(0)
const previewing = ref(false)
const importing = ref(false)
const error = ref('')
const job = ref<Job | null>(null)
let timer = 0

const progress = computed(() => {
  if (!job.value || !job.value.total) return 0
  return Math.round((job.value.done / job.value.total) * 100)
})
const jobDone = computed(() => !!job.value && ['succeeded', 'failed', 'partial'].includes(job.value.status))

function chooseFile(event: Event) {
  const f = (event.target as HTMLInputElement).files?.[0]
  if (f) setFile(f)
}

function onDrop(event: DragEvent) {
  event.preventDefault()
  const f = event.dataTransfer?.files?.[0]
  if (f) setFile(f)
}

function setFile(f: File) {
  file.value = f
  preview.value = []
  job.value = null
  error.value = ''
  total.value = 0
  importable.value = 0
  invalid.value = 0
}

async function doPreview() {
  if (!file.value) return
  previewing.value = true
  error.value = ''
  try {
    const res = await api.upload<{ rows: ImportRow[]; total: number; importable: number; invalid: number }>(
      '/tenants/import/preview',
      file.value,
    )
    preview.value = res.rows
    total.value = res.total
    importable.value = res.importable
    invalid.value = res.invalid
  } catch (e) {
    error.value = errorMessage(e, '预览失败')
  } finally {
    previewing.value = false
  }
}

async function startImport() {
  if (!file.value) return
  importing.value = true
  error.value = ''
  try {
    const res = await api.upload<{ job: Job }>('/tenants/import', file.value)
    job.value = res.job
    toast('导入任务已开始', 'success')
    pollJob()
  } catch (e) {
    error.value = errorMessage(e, '导入失败')
  } finally {
    importing.value = false
  }
}

function pollJob() {
  window.clearInterval(timer)
  timer = window.setInterval(async () => {
    if (!job.value) return
    try {
      const res = await api.get<{ job: Job }>(`/jobs/${job.value.id}`)
      job.value = res.job
      if (['succeeded', 'failed', 'partial'].includes(res.job.status)) {
        window.clearInterval(timer)
        if (res.job.status === 'succeeded') {
          toast('导入完成', 'success')
        } else {
          toast(`导入结束：成功 ${res.job.done - res.job.failed}，失败 ${res.job.failed}`, 'error')
        }
      }
    } catch {
      window.clearInterval(timer)
    }
  }, 1000)
}

onBeforeUnmount(() => window.clearInterval(timer))

function downloadResult(name: 'success.csv' | 'failures.csv') {
  if (!job.value) return
  downloadFile(`/jobs/${job.value.id}/artifacts/${name}`, name).catch((e) =>
    toast(errorMessage(e, '下载失败'), 'error'),
  )
}

function downloadSample() {
  const csv = 'username,display_name,notes\nzhangsan,张三,研发部\nlisi,李四,产品部\n'
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'clawbench-fleet-import-sample.csv'
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div style="max-width: 980px">
    <div class="card">
      <div class="card-header">
        <div>
          <div class="card-title">第一步：选择 CSV</div>
          <div class="card-sub">一行一个员工，必须有 username 列，display_name 和 notes 可选</div>
        </div>
        <button class="btn btn-sm" type="button" @click="downloadSample"><Download :size="14" /> 下载示例</button>
      </div>

      <label
        class="dropzone"
        @dragover.prevent
        @drop="onDrop"
      >
        <input type="file" accept=".csv,text/csv" style="display: none" @change="chooseFile" />
        <UploadCloud :size="30" style="opacity: 0.7" />
        <div style="font-weight: 600; margin-top: 8px">
          {{ file ? file.name : '把 CSV 拖到这里，或者点击选择' }}
        </div>
        <div class="small muted-2" style="margin-top: 3px">
          {{ file ? `${(file.size / 1024).toFixed(1)} KB` : 'UTF-8 编码，最多 1000 行' }}
        </div>
      </label>

      <div class="row-between" style="margin-top: 14px">
        <div class="small muted-2">导入前会先检查重名、格式和端口是否够用，不会直接写数据。</div>
        <button class="btn btn-primary" type="button" :disabled="!file || previewing" @click="doPreview">
          {{ previewing ? '检查中…' : '预览' }}
        </button>
      </div>

      <div v-if="error" class="form-error" style="margin-top: 12px">{{ error }}</div>
    </div>

    <div v-if="preview.length" class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">第二步：确认名单</div>
          <div class="card-sub">
            共 {{ total }} 行，可导入 {{ importable }} 行
            <span v-if="invalid" style="color: var(--red)"> · {{ invalid }} 行有问题</span>
          </div>
        </div>
        <button
          class="btn btn-primary"
          type="button"
          :disabled="importing || importable === 0"
          @click="startImport"
        >
          <FileSpreadsheet :size="15" />
          {{ importing ? '正在提交…' : `开始导入 ${importable} 个` }}
        </button>
      </div>

      <div class="table-wrap" style="max-height: 420px; overflow: auto">
        <table class="table">
          <thead><tr><th>行号</th><th>登录名</th><th>显示名</th><th>备注</th><th>检查结果</th></tr></thead>
          <tbody>
            <tr v-for="row in preview" :key="row.row">
              <td class="small muted-2">{{ row.row }}</td>
              <td class="mono small">{{ row.username || '—' }}</td>
              <td class="small">{{ row.display_name || '—' }}</td>
              <td class="small">{{ row.notes || '—' }}</td>
              <td>
                <span v-if="row.error" class="badge badge-failed">{{ row.error }}</span>
                <span v-else class="badge badge-ok">可以导入</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="job" class="card" style="margin-top: 16px">
      <div class="card-header">
        <div>
          <div class="card-title">第三步：导入进度</div>
          <div class="card-sub">
            {{ job.done }} / {{ job.total }}
            <span v-if="job.failed" style="color: var(--red)"> · 失败 {{ job.failed }}</span>
          </div>
        </div>
        <span class="badge" :class="job.status === 'succeeded' ? 'badge-ok' : job.status === 'failed' ? 'badge-failed' : 'badge-neutral'">
          {{ job.status }}
        </span>
      </div>

      <div class="progress" style="margin-bottom: 14px"><span :style="{ width: progress + '%' }" /></div>

      <div v-if="job.items?.length" class="table-wrap" style="max-height: 360px; overflow: auto">
        <table class="table">
          <thead><tr><th>登录名</th><th>状态</th><th>错误</th></tr></thead>
          <tbody>
            <tr v-for="item in job.items" :key="item.id">
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

      <div v-if="jobDone" class="row-between" style="margin-top: 14px; flex-wrap: wrap">
        <div class="small muted-2">成功清单里有明文密码，下载后请尽快发给员工并删除文件。</div>
        <div class="btn-row">
          <button class="btn btn-sm" type="button" @click="downloadResult('failures.csv')"><Download :size="14" /> 失败清单</button>
          <button class="btn btn-sm btn-primary" type="button" @click="downloadResult('success.csv')">
            <CheckCircle2 :size="14" /> 成功清单（含密码）
          </button>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px; border-color: rgba(245,185,76,.3)">
      <div class="row small" style="color: var(--amber); align-items: flex-start">
        <AlertTriangle :size="16" style="flex: 0 0 auto; margin-top: 1px" />
        <span>
          导入会真实创建 Linux 用户和 systemd 服务。成功后平台不保存明文密码，成功清单只保留在内存里 24 小时，丢了只能一个个重置。
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dropzone {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 150px;
  padding: 24px;
  border: 1px dashed var(--border);
  border-radius: var(--radius);
  background: rgba(9, 15, 28, 0.4);
  cursor: pointer;
  text-align: center;
  transition: border-color 0.15s, background 0.15s;
}
.dropzone:hover {
  border-color: rgba(91, 140, 255, 0.55);
  background: rgba(91, 140, 255, 0.06);
}
</style>
