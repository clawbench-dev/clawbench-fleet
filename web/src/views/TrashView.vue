<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { AlertTriangle, RotateCcw, Trash2 } from 'lucide-vue-next'
import { api } from '../api'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import CopyField from '../components/CopyField.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import { toast } from '../stores/toast'
import type { Tenant, TrashItem } from '../types'
import { formatBytes, formatTime } from '../utils'

const items = ref<TrashItem[]>([])
const loading = ref(false)
const restoreTarget = ref<TrashItem | null>(null)
const purgeTarget = ref<TrashItem | null>(null)
const password = ref('')

async function load() {
  loading.value = true
  try {
    const res = await api.get<{ items: TrashItem[] }>('/trash')
    items.value = res.items || []
  } catch (e: any) {
    toast(e?.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function doRestore() {
  if (!restoreTarget.value) return
  const target = restoreTarget.value
  restoreTarget.value = null
  try {
    const res = await api.post<{ tenant: Tenant; password?: string; warning?: string }>(`/trash/${target.id}/restore`)
    if (res.password) {
      password.value = res.password
    } else {
      toast('租户已恢复', 'success')
    }
    if (res.warning) toast(res.warning, 'error')
    load()
  } catch (e: any) {
    toast(e?.message || '恢复失败', 'error')
  }
}

async function doPurge() {
  if (!purgeTarget.value) return
  const target = purgeTarget.value
  purgeTarget.value = null
  try {
    await api.del(`/trash/${target.id}`)
    toast('已强制删除', 'success')
    load()
  } catch (e: any) {
    toast(e?.message || '删除失败', 'error')
  }
}
</script>

<template>
  <div>
    <div class="card">
      <div class="card-header">
        <div>
          <div class="card-title">已删除的租户</div>
          <div class="card-sub">删除时整个家目录会挪进来，默认保留 30 天后自动清理；期间可以恢复账号和端口，也可以立即强制删除</div>
        </div>
        <button class="btn btn-sm" type="button" :disabled="loading" @click="load">刷新</button>
      </div>

      <div v-if="items.length" class="table-wrap">
        <table class="table">
          <thead><tr><th>租户</th><th>删除时间</th><th>占用空间</th><th>剩余</th><th>端口</th><th style="text-align: right">操作</th></tr></thead>
          <tbody>
            <tr v-for="t in items" :key="t.id">
              <td>
                <span class="mono">{{ t.username }}</span>
                <div class="small muted-2">{{ t.display_name || '—' }}</div>
              </td>
              <td class="small">{{ formatTime(t.deleted_at || '') }}</td>
              <td class="small num">{{ formatBytes(t.disk_bytes) }}</td>
              <td>
                <span class="badge" :class="t.days_left <= 3 ? 'badge-failed' : 'badge-neutral'">{{ t.days_left }} 天</span>
              </td>
              <td class="mono small">{{ t.port }}</td>
              <td>
                <div class="actions">
                  <button class="btn btn-sm" type="button" @click="restoreTarget = t">
                    <RotateCcw :size="13" /> 恢复
                  </button>
                  <button class="btn btn-danger btn-sm" type="button" @click="purgeTarget = t">
                    <Trash2 :size="13" /> 强制删除
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else title="回收站是空的" hint="删除租户后会先放到这里，30 天内都能恢复。" />
    </div>

    <div class="card" style="margin-top: 16px; border-color: rgba(245,185,76,.3)">
      <div class="row small" style="color: var(--amber); align-items: flex-start">
        <AlertTriangle :size="16" style="flex: 0 0 auto; margin-top: 1px" />
        <span>恢复时会重新创建 Linux 用户。如果原来的 UID 被别的账号占了，会用新的 UID，并把家目录属主改过来。</span>
      </div>
    </div>

    <ConfirmDialog
      :open="!!restoreTarget"
      title="恢复租户"
      :message="`把 ${restoreTarget?.username || ''} 的账号和数据恢复回来，端口保持不变。\n\n如果同名账号已经被占用，恢复会失败。`"
      confirm-text="确认恢复"
      @cancel="restoreTarget = null"
      @confirm="doRestore"
    />

    <ConfirmDialog
      :open="!!purgeTarget"
      title="强制删除（跳过 30 天）"
      :message="`立即永久删除 ${purgeTarget?.username || ''} 的家目录、项目代码和所有数据，不能恢复。\n\n这个操作会跳过剩余的保留期。`"
      :require-text="purgeTarget?.username"
      confirm-text="永久删除"
      danger
      @cancel="purgeTarget = null"
      @confirm="doPurge"
    />

    <Modal v-if="password" title="租户已恢复，生成了新密码" @close="password = ''">
      <CopyField label="新的登录密码（只显示这一次）" :value="password" />
      <div class="small muted-2" style="margin-top: 12px">原配置文件不在了，所以平台生成了一个新密码。请复制发给员工。</div>
      <template #footer>
        <button class="btn btn-primary" type="button" @click="password = ''">我已保存</button>
      </template>
    </Modal>
  </div>
</template>
