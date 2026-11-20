import { createRouter, createWebHistory } from 'vue-router'

import AuditView from './views/AuditView.vue'
import ImportView from './views/ImportView.vue'
import LoginView from './views/LoginView.vue'
import OverviewView from './views/OverviewView.vue'
import SettingsView from './views/SettingsView.vue'
import TenantCreateView from './views/TenantCreateView.vue'
import TenantDetailView from './views/TenantDetailView.vue'
import TenantsView from './views/TenantsView.vue'
import TrashView from './views/TrashView.vue'
import UpgradesView from './views/UpgradesView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: LoginView, meta: { title: '登录' } },
    {
      path: '/',
      component: OverviewView,
      meta: { title: '总览', sub: '机器负载、实例状态和最近操作' },
    },
    {
      path: '/tenants',
      component: TenantsView,
      meta: { title: '租户', sub: '创建、启停、批量操作和日常巡检' },
    },
    {
      path: '/tenants/new',
      component: TenantCreateView,
      meta: { title: '新建租户', sub: '为一个员工创建 Linux 账号和 Clawbench 实例' },
    },
    {
      path: '/tenants/:id',
      component: TenantDetailView,
      meta: { title: '租户详情', sub: '状态、日志、备份和操作' },
    },
    {
      path: '/import',
      component: ImportView,
      meta: { title: '批量导入', sub: '用 CSV 一次创建多个租户' },
    },
    {
      path: '/upgrades',
      component: UpgradesView,
      meta: { title: '升级中心', sub: '从 npm 安装或上传版本、试点、批量升级和回滚' },
    },
    {
      path: '/trash',
      component: TrashView,
      meta: { title: '回收站', sub: '误删的租户在这里保留 30 天' },
    },
    {
      path: '/audit',
      component: AuditView,
      meta: { title: '审计日志', sub: '谁在什么时候对谁做了什么' },
    },
    {
      path: '/settings',
      component: SettingsView,
      meta: { title: '设置', sub: '端口、配额、备份和管理员密码' },
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})
