<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Server } from 'lucide-vue-next'
import { api } from '../api'
import { login } from '../stores/auth'

const router = useRouter()
const username = ref('admin')
const password = ref('')
const error = ref('')
const loading = ref(false)
const mode = ref('')

onMounted(async () => {
  try {
    const health = await api.get<{ mode: string }>('/health')
    mode.value = health.mode
  } catch {
    // Health is optional on the login screen.
  }
})

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await login(username.value.trim(), password.value)
    router.push('/')
  } catch (e: any) {
    error.value = e?.message || '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <form class="login-card" @submit.prevent="submit">
      <div class="login-brand">
        <div class="brand-mark"><Server :size="20" /></div>
        <div>
          <div class="login-title">Clawbench Fleet</div>
          <div class="login-sub">企业实例管理平台</div>
        </div>
      </div>

      <div class="field">
        <label>管理员账号</label>
        <input v-model="username" class="input" autocomplete="username" />
      </div>
      <div class="field">
        <label>密码</label>
        <input v-model="password" type="password" class="input" autocomplete="current-password" />
      </div>

      <div v-if="error" class="form-error" style="margin-top: 10px">{{ error }}</div>

      <button class="btn btn-primary" type="submit" :disabled="loading">
        {{ loading ? '登录中…' : '登录' }}
      </button>

      <div v-if="mode === 'dev'" class="login-hint">开发模式默认账号 admin，密码 admin123</div>

      <router-link class="guest-entry" to="/guest">我是员工，只看实例地址 →</router-link>
    </form>
  </div>
</template>
