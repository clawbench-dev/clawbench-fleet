<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ChevronLeft, ChevronRight, RefreshCw, Search } from 'lucide-vue-next'
import { api } from '../api'
import EmptyState from '../components/EmptyState.vue'
import { toast } from '../stores/toast'
import type { AuditEntry, Page } from '../types'
import { actionLabel, formatTime } from '../utils'

const items = ref<AuditEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 50
const actor = ref('')
const action = ref('')
const tenant = ref('')
const from = ref('')
const to = ref('')
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (actor.value.trim()) params.set('actor', actor.value.trim())
    if (action.value.trim()) params.set('action', action.value.trim())
    if (tenant.value.trim()) params.set('tenant', tenant.value.trim())
    if (from.value) params.set('from', from.value)
    if (to.value) params.set('to', to.value + 'T23:59:59')
    const res = await api.get<Page<AuditEntry>>('/audit?' + params.toString())
    items.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast(e?.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

function applyFilters() {
  page.value = 1
  load()
}

function reset() {
  actor.value = ''
  action.value = ''
  tenant.value = ''
  from.value = ''
  to.value = ''
  page.value = 1
  load()
}

const totalPages = () => Math.max(1, Math.ceil(total.value / pageSize))

function go(delta: number) {
  page.value = Math.max(1, Math.min(totalPages(), page.value + delta))
  load()
}
</script>

<template>
  <div>
    <div class="card">
      <div class="row" style="flex-wrap: wrap; align-items: flex-end">
        <div class="field" style="min-width: 140px; flex: 1">
          <label>操作人</label>
          <input v-model="actor" class="input" placeholder="admin" @keyup.enter="applyFilters" />
        </div>
        <div class="field" style="min-width: 160px; flex: 1">
          <label>动作</label>
          <input v-model="action" class="input mono" placeholder="tenant.restart" @keyup.enter="applyFilters" />
        </div>
        <div class="field" style="min-width: 140px; flex: 1">
          <label>租户</label>
          <input v-model="tenant" class="input" placeholder="登录名" @keyup.enter="applyFilters" />
        </div>
        <div class="field">
          <label>开始</label>
          <input v-model="from" class="input" type="date" />
        </div>
        <div class="field">
          <label>结束</label>
          <input v-model="to" class="input" type="date" />
        </div>
        <div class="btn-row" style="padding-bottom: 1px">
          <button class="btn btn-primary" type="button" @click="applyFilters"><Search :size="15" /> 查询</button>
          <button class="btn" type="button" @click="reset">重置</button>
          <button class="btn btn-ghost btn-icon" type="button" title="刷新" @click="load">
            <RefreshCw :size="15" />
          </button>
        </div>
      </div>
    </div>

    <div v-if="items.length" class="table-wrap" style="margin-top: 12px">
      <table class="table">
        <thead>
          <tr><th>时间</th><th>操作人</th><th>动作</th><th>对象</th><th>结果</th><th>IP</th><th>详情</th></tr>
        </thead>
        <tbody>
          <tr v-for="a in items" :key="a.id">
            <td class="small" style="white-space: nowrap">{{ formatTime(a.created_at) }}</td>
            <td class="small mono">{{ a.actor }}</td>
            <td class="small">{{ actionLabel(a.action) }}<div class="small muted-2 mono">{{ a.action }}</div></td>
            <td class="small">{{ a.tenant_name || '—' }}</td>
            <td>
              <span class="badge" :class="a.result === 'ok' ? 'badge-ok' : 'badge-failed'">{{ a.result === 'ok' ? '成功' : '失败' }}</span>
              <div v-if="a.error" class="small" style="color: #ffb3b3; max-width: 260px">{{ a.error }}</div>
            </td>
            <td class="small mono">{{ a.ip || '—' }}</td>
            <td class="small">
              <details v-if="a.detail && a.detail !== '{}'">
                <summary class="muted" style="cursor: pointer">查看</summary>
                <pre class="pre" style="margin-top: 6px; max-width: 360px">{{ a.detail }}</pre>
              </details>
              <span v-else class="muted-2">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="!loading" style="margin-top: 12px">
      <EmptyState title="没有匹配的记录" hint="换个筛选条件试试。" />
    </div>

    <div v-if="total > pageSize" class="row-between" style="margin-top: 14px">
      <span class="small muted-2">共 {{ total }} 条，第 {{ page }} / {{ totalPages() }} 页</span>
      <div class="btn-row">
        <button class="btn btn-sm" type="button" :disabled="page <= 1" @click="go(-1)"><ChevronLeft :size="14" /> 上一页</button>
        <button class="btn btn-sm" type="button" :disabled="page >= totalPages()" @click="go(1)">下一页 <ChevronRight :size="14" /></button>
      </div>
    </div>
  </div>
</template>
