<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { AlertTriangle, ArrowRight, CheckCircle2, Loader2, Plus } from 'lucide-vue-next'
import { api } from '../api'
import CopyField from '../components/CopyField.vue'
import Modal from '../components/Modal.vue'
import { toast } from '../stores/toast'
import type { Tenant } from '../types'

const router = useRouter()

const form = ref({
  username: '',
  display_name: '',
  notes: '',
  cpu_quota: '',
  memory_max: '',
  tasks_max: 0,
})
const submitting = ref(false)
const error = ref('')
const created = ref<{ tenant: Tenant; password: string; warning?: string; pending?: boolean } | null>(null)

async function submit() {
  error.value = ''
  submitting.value = true
  try {
    const body: Record<string, unknown> = {
      username: form.value.username.trim(),
      display_name: form.value.display_name.trim(),
      notes: form.value.notes.trim(),
    }
    if (form.value.cpu_quota.trim()) body.cpu_quota = form.value.cpu_quota.trim()
    if (form.value.memory_max.trim()) body.memory_max = form.value.memory_max.trim()
    if (form.value.tasks_max > 0) body.tasks_max = form.value.tasks_max
    const res = await api.post<{ tenant: Tenant; password: string; warning?: string; pending?: boolean }>('/tenants', body)
    created.value = res
    toast(res.pending ? '已受理，实例正在后台创建' : '租户创建成功', 'success')
  } catch (e: any) {
    error.value = e?.message || '创建失败'
  } finally {
    submitting.value = false
  }
}

function goDetail() {
  if (created.value) router.push(`/tenants/${created.value.tenant.id}`)
}

function createAnother() {
  created.value = null
  form.value = { username: '', display_name: '', notes: '', cpu_quota: '', memory_max: '', tasks_max: 0 }
}
</script>

<template>
  <div style="max-width: 760px">
    <div class="card">
      <div class="card-header">
        <div>
          <div class="card-title">基本信息</div>
          <div class="card-sub">登录名会成为 Linux 用户名，创建后不能修改</div>
        </div>
      </div>

      <div class="form-grid">
        <div class="field">
          <label>登录名 *</label>
          <input v-model="form.username" class="input mono" placeholder="例如 zhangsan" autocomplete="off" />
          <div class="form-hint">3-32 位，小写字母开头，只能包含小写字母、数字、下划线和短横线</div>
        </div>
        <div class="field">
          <label>显示名</label>
          <input v-model="form.display_name" class="input" placeholder="例如 张三" />
        </div>
        <div class="field span-2">
          <label>备注</label>
          <textarea v-model="form.notes" class="textarea" placeholder="部门、用途等，只有管理员能看到" />
        </div>
      </div>

      <div class="divider" />

      <div class="card-header" style="margin-bottom: 12px">
        <div>
          <div class="card-title">资源上限</div>
          <div class="card-sub">留空则使用平台默认值；修改后会自动重启一次实例</div>
        </div>
      </div>

      <div class="form-grid">
        <div class="field">
          <label>CPU 上限</label>
          <input v-model="form.cpu_quota" class="input mono" placeholder="200%" />
        </div>
        <div class="field">
          <label>内存上限</label>
          <input v-model="form.memory_max" class="input mono" placeholder="4G" />
        </div>
        <div class="field">
          <label>进程数上限</label>
          <input v-model.number="form.tasks_max" class="input mono" type="number" min="0" placeholder="512" />
        </div>
      </div>

      <div v-if="error" class="form-error" style="margin-top: 14px">{{ error }}</div>

      <div class="form-actions">
        <router-link class="btn" to="/tenants">取消</router-link>
        <button class="btn btn-primary" type="button" :disabled="submitting || !form.username.trim()" @click="submit">
          <Plus :size="15" />
          {{ submitting ? '正在创建…' : '创建租户' }}
        </button>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="small muted">
        创建过程会自动完成：建 Linux 账号 → 分配端口 → 写配置和 systemd 单元 → 启动实例 → 健康检查。
        成功后只会显示一次初始密码，请立刻复制发给员工。
      </div>
    </div>

    <Modal v-if="created" :title="created.pending ? '已提交，正在后台创建' : '租户创建成功'" wide @close="goDetail">
      <div class="row" style="gap: 8px; margin-bottom: 14px">
        <CheckCircle2 v-if="!created.pending" :size="18" color="var(--green)" />
        <Loader2 v-else :size="18" color="var(--amber)" />
        <strong>{{ created.tenant.username }}</strong>
        <span class="small muted">{{ created.pending ? '实例启动中，稍后刷新列表看结果' : '已创建' }}</span>
      </div>

      <div v-if="created.warning" class="card" style="border-color: rgba(245,185,76,.4); padding: 12px; margin-bottom: 14px">
        <div class="row small" style="color: var(--amber)"><AlertTriangle :size="15" /> {{ created.warning }}</div>
      </div>

      <div class="stack">
        <CopyField label="访问地址" :value="created.tenant.address" />
        <CopyField label="初始密码（只显示这一次）" :value="created.password" />
      </div>

      <div class="small muted-2" style="margin-top: 14px">
        <template v-if="created.pending">
          账号已经建好，实例还在后台启动（一般十几秒到一分钟）。现在就可以把地址和密码发给员工，等列表里的状态变成“运行中”再让员工登录。
        </template>
        <template v-else>
          员工打开地址后用这个密码登录。密码丢了只能重置。
        </template>
      </div>

      <template #footer>
        <button class="btn" type="button" @click="createAnother">继续创建</button>
        <button class="btn btn-primary" type="button" @click="goDetail">
          打开详情 <ArrowRight :size="15" />
        </button>
      </template>
    </Modal>
  </div>
</template>
