<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowUpCircle,
  LayoutDashboard,
  LogOut,
  Menu,
  ScrollText,
  Server,
  Settings,
  Trash2,
  Upload,
  Users,
} from 'lucide-vue-next'
import { auth, logout } from '../stores/auth'

const route = useRoute()
const router = useRouter()
const open = ref(false)

const nav = [
  { to: '/', label: '总览', icon: LayoutDashboard },
  { to: '/tenants', label: '租户', icon: Users },
  { to: '/import', label: '批量导入', icon: Upload },
  { to: '/upgrades', label: '升级中心', icon: ArrowUpCircle },
  { to: '/trash', label: '回收站', icon: Trash2 },
  { to: '/audit', label: '审计日志', icon: ScrollText },
  { to: '/settings', label: '设置', icon: Settings },
]

const title = computed(() => (route.meta.title as string) || '')
const sub = computed(() => (route.meta.sub as string) || '')

watch(
  () => route.fullPath,
  () => (open.value = false),
)

async function doLogout() {
  await logout()
  router.push('/login')
}
</script>

<template>
  <div class="shell">
    <aside class="sidebar" :class="{ open }">
      <div class="brand">
        <div class="brand-mark"><Server :size="19" /></div>
        <div>
          <div class="brand-title">Clawbench Fleet</div>
          <div class="brand-sub">Instance Console</div>
        </div>
      </div>

      <nav class="nav">
        <router-link
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: item.to === '/' ? route.path === '/' : route.path.startsWith(item.to) }"
        >
          <component :is="item.icon" />
          <span>{{ item.label }}</span>
        </router-link>
      </nav>

      <div class="sidebar-footer">
        <div class="admin-chip">
          <div class="avatar">{{ (auth.admin?.username || 'A').slice(0, 1).toUpperCase() }}</div>
          <div style="min-width: 0">
            <div class="admin-name">{{ auth.admin?.username || 'admin' }}</div>
            <div class="admin-role">超级管理员</div>
          </div>
        </div>
        <button class="btn btn-ghost" style="width: 100%; margin-top: 6px" type="button" @click="doLogout">
          <LogOut />
          退出登录
        </button>
      </div>
    </aside>

    <div v-if="open" class="sidebar-scrim" @click="open = false" />

    <div class="main">
      <header class="topbar">
        <button class="btn btn-ghost btn-icon menu-btn" type="button" @click="open = !open">
          <Menu />
        </button>
        <div>
          <div class="topbar-title">{{ title }}</div>
          <div v-if="sub" class="topbar-sub">{{ sub }}</div>
        </div>
        <div class="topbar-spacer" />
        <div class="row small muted-2">
          <Server :size="14" />
          <span>管理控制台</span>
        </div>
      </header>

      <main class="content">
        <slot />
      </main>
    </div>
  </div>
</template>
