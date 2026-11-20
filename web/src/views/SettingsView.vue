<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { KeyRound, Save, Server, ShieldCheck } from 'lucide-vue-next'
import { api } from '../api'
import { auth } from '../stores/auth'
import { toast } from '../stores/toast'
import type { Settings } from '../types'

const router = useRouter()
const settings = ref<Settings | null>(null)
const meta = ref<{
  mode: string
  public_host: string
  install_dir: string
  current_version: string
  fleet_version: string
} | null>(null)
const saving = ref(false)
const pwd = ref({ current_password: '', new_password: '', confirm: '' })
const pwdSaving = ref(false)

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
    }>('/settings')
    settings.value = res.settings
    meta.value = {
      mode: res.mode,
      public_host: res.public_host,
      install_dir: res.install_dir,
      current_version: res.current_version,
      fleet_version: res.fleet_version,
    }
  } catch (e: any) {
    toast(e?.message || '加载失败', 'error')
  }
}

onMounted(load)

async function save() {
  if (!settings.value) return
  saving.value = true
  try {
    const res = await api.patch<{ settings: Settings }>('/settings', settings.value)
    settings.value = res.settings
    toast('设置已保存', 'success')
  } catch (e: any) {
    toast(e?.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
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
  } catch (e: any) {
    toast(e?.message || '修改失败', 'error')
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
        </div>
        <div class="form-grid">
          <div class="field"><label>CPU 上限</label><input v-model="settings.default_cpu_quota" class="input mono" placeholder="200%" /></div>
          <div class="field"><label>内存上限</label><input v-model="settings.default_memory_max" class="input mono" placeholder="4G" /></div>
          <div class="field"><label>进程数上限</label><input v-model.number="settings.default_tasks_max" class="input mono" type="number" min="1" /></div>
          <div class="field">
            <label>磁盘提醒阈值（GB，0=关闭）</label>
            <input v-model.number="settings.disk_warn_gb" class="input mono" type="number" min="0" />
          </div>
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
          <div class="card-title">对外访问</div>
          <div class="card-sub">员工拿到的地址会显示成这个主机名或 IP</div>
        </div>
      </div>
      <div class="form-grid">
        <div class="field">
          <label>对外主机名 / IP</label>
          <input v-model="settings.public_host" class="input mono" placeholder="留空则自动检测本机内网 IP" />
        </div>
        <div class="field">
          <label>租户端口转发</label>
          <label class="row small" style="height: 38px">
            <input v-model="settings.allow_port_forward" class="checkbox" type="checkbox" />
            允许员工在自己的实例里开端口转发
          </label>
        </div>
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
  </div>
</template>
