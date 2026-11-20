<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from './components/AppShell.vue'
import ToastHost from './components/ToastHost.vue'
import { auth, loadMe } from './stores/auth'

const route = useRoute()
const router = useRouter()

// Pages anyone may open without logging in.
const publicPaths = ['/login', '/guest']

onMounted(async () => {
  await loadMe()
  if (!auth.admin && !publicPaths.includes(route.path)) {
    router.replace('/login')
  }
})

window.addEventListener('fleet:unauthorized', () => {
  auth.admin = null
  if (!publicPaths.includes(route.path)) {
    router.replace('/login')
  }
})
</script>

<template>
  <div v-if="!auth.ready" class="login-page">
    <div class="muted">加载中…</div>
  </div>
  <router-view v-else-if="publicPaths.includes(route.path)" />
  <AppShell v-else>
    <router-view />
  </AppShell>
  <ToastHost />
</template>
