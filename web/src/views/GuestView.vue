<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertTriangle, ChevronLeft, ChevronRight, Megaphone, RefreshCw, Search, Server } from 'lucide-vue-next'
import { api, errorMessage } from '../api'
import LoadingState from '../components/LoadingState.vue'
import Modal from '../components/Modal.vue'
import TenantTable from '../components/TenantTable.vue'
import type { Row } from '../components/TenantTable.vue'
import { toast } from '../stores/toast'
import type { GuestPage } from '../types'

const page = ref<GuestPage | null>(null)
const loading = ref(true)
const error = ref('')
const q = ref('')
const status = ref('')
const pageNo = ref(1)
const pageSize = 20

const reportTarget = ref<Row | null>(null)
const reportMessage = ref('')
const submitting = ref(false)

let timer = 0
let searchTimer = 0

const defaultHelp = `1. 在上面找到自己的名字，点链接就能进自己的 Clawbench。
2. 地址也可以复制下来，存到浏览器书签。
3. 打不开时先看状态：显示「已停止」说明服务被关掉了，点「打不开？」告诉管理员。
4. 忘记密码、需要装东西，都找管理员，不要自己乱改服务器。`

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const params = new URLSearchParams({ page: String(pageNo.value), page_size: String(pageSize) })
    if (q.value.trim()) params.set('q', q.value.trim())
    if (status.value) params.set('status', status.value)
    page.value = await api.get<GuestPage>('/public/instances?' + params.toString())
    error.value = ''
  } catch (e) {
    if (!silent) error.value = errorMessage(e, '加载失败')
  } finally {
    if (!silent) loading.value = false
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(() => load(true), 15000)
})
onBeforeUnmount(() => {
  window.clearInterval(timer)
  window.clearTimeout(searchTimer)
})

watch([q, status], () => {
  pageNo.value = 1
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => load(), 300)
})
watch(pageNo, () => load())

const items = computed<Row[]>(() => page.value?.items || [])
const total = computed(() => page.value?.total || 0)
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))

function openReport(t: Row) {
  reportTarget.value = t
  reportMessage.value = ''
}

async function submitReport() {
  if (!reportTarget.value) return
  submitting.value = true
  try {
    await api.post('/public/reports', {
      tenant_id: reportTarget.value.id,
      message: reportMessage.value.trim(),
    })
    toast('已告诉管理员，稍后会有人处理', 'success')
    reportTarget.value = null
  } catch (e) {
    toast(errorMessage(e, '提交失败，请稍后再试'), 'error')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="guest-page">
    <header class="guest-head">
      <div class="guest-brand">
        <div class="brand-mark"><Server :size="19" /></div>
        <div>
          <div class="guest-title">Clawbench 实例地址</div>
          <div class="guest-sub">找到自己的名字，点链接就能用</div>
        </div>
      </div>
      <button class="btn btn-ghost" type="button" :disabled="loading" @click="load()">
        <RefreshCw :size="15" />
        刷新
      </button>
    </header>

    <div v-if="error" class="card" style="border-color: rgba(255, 107, 107, 0.4)">
      <div class="row"><AlertTriangle :size="18" color="var(--red)" /> {{ error }}</div>
    </div>

    <div v-else-if="page && !page.enabled" class="card guest-disabled">
      <AlertTriangle :size="22" color="var(--amber)" />
      <div>
        <div class="card-title">这个页面还没有开启</div>
        <div class="card-sub">请联系管理员，让他到「设置 → 员工自助页」里打开。</div>
      </div>
    </div>

    <template v-else-if="page">
      <div v-if="page.notice" class="guest-notice">
        <Megaphone :size="17" />
        <div class="guest-notice-text">{{ page.notice }}</div>
      </div>

      <div class="card">
        <div class="row-between" style="flex-wrap: wrap; gap: 12px">
          <div class="row" style="flex: 1; min-width: 260px">
            <div style="position: relative; flex: 1">
              <Search :size="15" style="position: absolute; left: 11px; top: 11px; color: var(--muted-2)" />
              <input v-model="q" class="input" style="padding-left: 33px" placeholder="搜自己的名字或登录名" />
            </div>
            <select v-model="status" class="select" style="width: 140px">
              <option value="">全部状态</option>
              <option value="running">运行中</option>
              <option value="stopped">已停止</option>
              <option value="error">异常</option>
              <option value="creating">创建中</option>
            </select>
          </div>
          <div class="small muted-2">共 {{ total }} 个</div>
        </div>
      </div>

      <TenantTable
        v-if="items.length"
        :items="items"
        reportable
        style="margin-top: 12px"
        @report="openReport"
      />

      <LoadingState v-else-if="loading" text="正在读取实例列表…" />

      <div v-else class="card guest-disabled" style="margin-top: 12px">
        <Search :size="20" color="var(--muted-2)" />
        <div>
          <div class="card-title">没找到</div>
          <div class="card-sub">{{ total ? '换个关键词试试，或者问管理员。' : '管理员还没有创建任何实例。' }}</div>
        </div>
      </div>

      <div v-if="total > pageSize" class="row-between" style="margin-top: 14px">
        <span class="small muted-2">第 {{ pageNo }} / {{ totalPages }} 页</span>
        <div class="btn-row">
          <button class="btn btn-sm" type="button" :disabled="pageNo <= 1" @click="pageNo--">
            <ChevronLeft :size="14" /> 上一页
          </button>
          <button class="btn btn-sm" type="button" :disabled="pageNo >= totalPages" @click="pageNo++">
            下一页 <ChevronRight :size="14" />
          </button>
        </div>
      </div>

      <div class="card guest-help">
        <div class="card-title" style="margin-bottom: 8px">使用说明</div>
        <div class="guest-help-text">{{ page.help || defaultHelp }}</div>
      </div>
    </template>

    <LoadingState v-else text="正在读取…" />

    <Modal v-if="reportTarget" title="报告问题" @close="reportTarget = null">
      <div class="stack">
        <div class="small muted">
          实例：<strong>{{ reportTarget.display_name || reportTarget.username }}</strong>
          <span class="mono muted-2">（端口 {{ reportTarget.port }}）</span>
        </div>
        <div class="field">
          <label>哪里不对？（可以不填）</label>
          <textarea
            v-model="reportMessage"
            class="textarea"
            maxlength="300"
            placeholder="例如：页面打不开 / 一直转圈 / 提示密码错误"
          />
        </div>
        <div class="small muted-2">提交后管理员会在后台看到，不会自动修复，请稍等。</div>
      </div>
      <template #footer>
        <button class="btn" type="button" @click="reportTarget = null">取消</button>
        <button class="btn btn-primary" type="button" :disabled="submitting" @click="submitReport">
          {{ submitting ? '提交中…' : '提交' }}
        </button>
      </template>
    </Modal>
  </div>
</template>
