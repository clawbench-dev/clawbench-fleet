<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ChevronLeft,
  ChevronRight,
  Play,
  Plus,
  RefreshCw,
  RotateCw,
  Search,
  Square,
  Upload,
} from 'lucide-vue-next'
import { api } from '../api'
import EmptyState from '../components/EmptyState.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { toast } from '../stores/toast'
import type { Job, Page, Tenant } from '../types'
import { formatBytes, formatPercent, formatTime } from '../utils'

const items = ref<Tenant[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const q = ref('')
const status = ref('')
const loading = ref(false)
const selected = ref<number[]>([])

let pollTimer = 0
let searchTimer = 0
let jobTimer = 0

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (q.value.trim()) params.set('q', q.value.trim())
    if (status.value) params.set('status', status.value)
    const res = await api.get<Page<Tenant>>('/tenants?' + params.toString())
    items.value = res.items
    total.value = res.total
    selected.value = selected.value.filter((id) => res.items.some((t) => t.id === id))
  } catch (e: any) {
    if (!silent) toast(e?.message || '加载失败', 'error')
  } finally {
    if (!silent) loading.value = false
  }
}

onMounted(() => {
  load()
  pollTimer = window.setInterval(() => load(true), 5000)
})

onBeforeUnmount(() => {
  window.clearInterval(pollTimer)
  window.clearInterval(searchTimer)
  window.clearInterval(jobTimer)
})

watch([q, status], () => {
  page.value = 1
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => load(), 300)
})

watch(page, () => load())

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
const allChecked = computed(() => items.value.length > 0 && items.value.every((t) => selected.value.includes(t.id)))

function toggleAll() {
  selected.value = allChecked.value ? [] : items.value.map((t) => t.id)
}

function toggle(id: number) {
  if (selected.value.includes(id)) {
    selected.value = selected.value.filter((x) => x !== id)
  } else {
    selected.value = [...selected.value, id]
  }
}

async function act(t: Tenant, action: 'start' | 'stop' | 'restart') {
  const labels = { start: '启动', stop: '停止', restart: '重启' }
  try {
    await api.post(`/tenants/${t.id}/${action}`)
    toast(`${t.username} ${labels[action]}成功`, 'success')
    load(true)
  } catch (e: any) {
    toast(e?.message || `${labels[action]}失败`, 'error')
    load(true)
  }
}

async function bulk(action: 'start' | 'stop' | 'restart') {
  if (!selected.value.length) return
  try {
    const res = await api.post<{ job: Job }>('/tenants/bulk', { action, ids: selected.value })
    toast(`批量任务已开始（${selected.value.length} 个）`, 'success')
    selected.value = []
    watchJob(res.job.id)
    load(true)
  } catch (e: any) {
    toast(e?.message || '批量操作失败', 'error')
  }
}

function watchJob(id: number) {
  window.clearInterval(jobTimer)
  jobTimer = window.setInterval(async () => {
    try {
      const res = await api.get<{ job: Job }>(`/jobs/${id}`)
      const job = res.job
      if (['succeeded', 'failed', 'partial'].includes(job.status)) {
        window.clearInterval(jobTimer)
        if (job.status === 'succeeded') {
          toast('批量任务完成', 'success')
        } else {
          toast(`批量任务结束：成功 ${job.done - job.failed}，失败 ${job.failed}`, 'error')
        }
        load(true)
      }
    } catch {
      window.clearInterval(jobTimer)
    }
  }, 1000)
}
</script>

<template>
  <div>
    <div class="card">
      <div class="row-between" style="flex-wrap: wrap; gap: 12px">
        <div class="row" style="flex: 1; min-width: 260px">
          <div style="position: relative; flex: 1">
            <Search :size="15" style="position: absolute; left: 11px; top: 11px; color: var(--muted-2)" />
            <input v-model="q" class="input" style="padding-left: 33px" placeholder="搜索登录名、显示名、备注" />
          </div>
          <select v-model="status" class="select" style="width: 140px">
            <option value="">全部状态</option>
            <option value="running">运行中</option>
            <option value="stopped">已停止</option>
            <option value="error">异常</option>
            <option value="creating">创建中</option>
          </select>
        </div>
        <div class="btn-row">
          <button class="btn" type="button" :disabled="loading" @click="load()">
            <RefreshCw :size="15" :class="{ spin: loading }" />
            刷新
          </button>
          <router-link class="btn" to="/import"><Upload :size="15" /> 批量导入</router-link>
          <router-link class="btn btn-primary" to="/tenants/new"><Plus :size="15" /> 新建租户</router-link>
        </div>
      </div>
    </div>

    <div v-if="selected.length" class="card" style="margin-top: 12px; padding: 12px 16px">
      <div class="row-between" style="flex-wrap: wrap">
        <div class="row">
          <span class="badge badge-neutral">已选 {{ selected.length }} 个</span>
          <button class="btn btn-sm" type="button" @click="bulk('start')"><Play :size="13" /> 启动</button>
          <button class="btn btn-sm" type="button" @click="bulk('stop')"><Square :size="13" /> 停止</button>
          <button class="btn btn-sm" type="button" @click="bulk('restart')"><RotateCw :size="13" /> 重启</button>
        </div>
        <button class="btn btn-ghost btn-sm" type="button" @click="selected = []">取消选择</button>
      </div>
    </div>

    <div v-if="items.length" class="table-wrap" style="margin-top: 12px">
      <table class="table">
        <thead>
          <tr>
            <th style="width: 36px"><input class="checkbox" type="checkbox" :checked="allChecked" @change="toggleAll" /></th>
            <th>租户</th>
            <th>访问地址</th>
            <th>状态</th>
            <th>版本</th>
            <th>CPU</th>
            <th>内存</th>
            <th>磁盘</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in items" :key="t.id">
            <td><input class="checkbox" type="checkbox" :checked="selected.includes(t.id)" @change="toggle(t.id)" /></td>
            <td>
              <router-link :to="`/tenants/${t.id}`" class="mono">{{ t.username }}</router-link>
              <div class="small muted-2">{{ t.display_name || '—' }}</div>
            </td>
            <td><a class="mono small" :href="t.address" target="_blank" rel="noreferrer">{{ t.address }}</a></td>
            <td><StatusBadge :status="t.status" /></td>
            <td class="mono small">{{ t.version || '—' }}</td>
            <td class="num small">{{ formatPercent(t.metrics?.cpu_percent || 0) }}</td>
            <td class="num small">{{ formatBytes(t.metrics?.memory_bytes || 0) }}</td>
            <td class="num small">{{ formatBytes(t.disk_bytes || 0) }}</td>
            <td>
              <div class="actions">
                <button
                  v-if="t.status === 'running'"
                  class="btn btn-ghost btn-sm btn-icon"
                  type="button"
                  title="停止"
                  @click="act(t, 'stop')"
                >
                  <Square :size="14" />
                </button>
                <button
                  v-else
                  class="btn btn-ghost btn-sm btn-icon"
                  type="button"
                  title="启动"
                  @click="act(t, 'start')"
                >
                  <Play :size="14" />
                </button>
                <button class="btn btn-ghost btn-sm btn-icon" type="button" title="重启" @click="act(t, 'restart')">
                  <RotateCw :size="14" />
                </button>
                <router-link class="btn btn-sm" :to="`/tenants/${t.id}`">详情</router-link>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="!loading" style="margin-top: 12px">
      <EmptyState title="没有找到租户" hint="换个搜索词，或者创建第一个实例。">
        <router-link class="btn btn-primary" to="/tenants/new"><Plus :size="15" /> 新建租户</router-link>
      </EmptyState>
    </div>

    <div v-if="total > pageSize" class="row-between" style="margin-top: 14px">
      <span class="small muted-2">共 {{ total }} 个，第 {{ page }} / {{ totalPages }} 页</span>
      <div class="btn-row">
        <button class="btn btn-sm" type="button" :disabled="page <= 1" @click="page--">
          <ChevronLeft :size="14" /> 上一页
        </button>
        <button class="btn btn-sm" type="button" :disabled="page >= totalPages" @click="page++">
          下一页 <ChevronRight :size="14" />
        </button>
      </div>
    </div>
  </div>
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
