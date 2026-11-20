<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { AlertTriangle, ExternalLink, Megaphone, RefreshCw, Search, Server, Wrench } from 'lucide-vue-next'
import { api } from '../api'
import CopyField from '../components/CopyField.vue'
import Modal from '../components/Modal.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { toast } from '../stores/toast'
import type { GuestInstance, GuestPage } from '../types'

const page = ref<GuestPage | null>(null)
const loading = ref(true)
const error = ref('')
const q = ref('')

const reportTarget = ref<GuestInstance | null>(null)
const reportMessage = ref('')
const submitting = ref(false)

let timer = 0

const defaultHelp = `1. 在上面找到自己的名字，点「打开」就能进自己的 Clawbench。
2. 地址也可以复制下来，存到浏览器书签。
3. 打不开时先看状态：显示「已停止」说明服务被关掉了，点「报障」告诉管理员。
4. 忘记密码、需要装东西，都找管理员，不要自己乱改服务器。`

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    page.value = await api.get<GuestPage>('/public/instances')
    error.value = ''
  } catch (e: any) {
    if (!silent) error.value = e?.message || '加载失败'
  } finally {
    if (!silent) loading.value = false
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(() => load(true), 15000)
})
onBeforeUnmount(() => window.clearInterval(timer))

const items = computed(() => page.value?.items || [])

const filtered = computed(() => {
  const key = q.value.trim().toLowerCase()
  if (!key) return items.value
  return items.value.filter((t) => {
    return (
      t.username.toLowerCase().includes(key) ||
      (t.display_name || '').toLowerCase().includes(key) ||
      String(t.port).includes(key)
    )
  })
})

const runningCount = computed(() => items.value.filter((t) => t.status === 'running').length)

function openReport(t: GuestInstance) {
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
  } catch (e: any) {
    toast(e?.message || '提交失败，请稍后再试', 'error')
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
          <div class="guest-sub">找到自己的名字，点开就能用</div>
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

      <div class="guest-toolbar">
        <div class="guest-search">
          <Search :size="16" />
          <input v-model="q" class="input" placeholder="搜自己的名字、登录名或端口" />
        </div>
        <div class="small muted-2">
          共 {{ items.length }} 个实例 · {{ runningCount }} 个运行中
        </div>
      </div>

      <div v-if="!filtered.length" class="card guest-disabled">
        <Search :size="20" color="var(--muted-2)" />
        <div>
          <div class="card-title">没找到</div>
          <div class="card-sub">{{ items.length ? '换个关键词试试，或者问管理员。' : '管理员还没有创建任何实例。' }}</div>
        </div>
      </div>

      <div v-else class="guest-grid">
        <div v-for="t in filtered" :key="t.id" class="card guest-card">
          <div class="guest-card-head">
            <div style="min-width: 0">
              <div class="guest-name">{{ t.display_name || t.username }}</div>
              <div class="small muted-2 mono">{{ t.username }}</div>
            </div>
            <StatusBadge :status="t.status" />
          </div>

          <div class="guest-port">
            <span class="small muted-2">端口</span>
            <span class="guest-port-num">{{ t.port }}</span>
          </div>

          <CopyField :value="t.address" />

          <div v-if="t.disk_warn" class="guest-warn">
            <AlertTriangle :size="14" />
            空间快满了，请清理一下或联系管理员
          </div>

          <div class="guest-actions">
            <a class="btn btn-primary btn-sm" :href="t.address" target="_blank" rel="noopener">
              <ExternalLink :size="14" />
              打开
            </a>
            <button class="btn btn-sm" type="button" @click="openReport(t)">
              <Wrench :size="14" />
              打不开？
            </button>
          </div>
        </div>
      </div>

      <div class="card guest-help">
        <div class="card-title" style="margin-bottom: 8px">使用说明</div>
        <div class="guest-help-text">{{ page.help || defaultHelp }}</div>
      </div>
    </template>

    <div v-else class="muted" style="padding: 40px; text-align: center">加载中…</div>

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
