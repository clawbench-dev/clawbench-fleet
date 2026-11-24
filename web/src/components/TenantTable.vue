<script setup lang="ts">
// 租户列表只写一份：管理员进来能看到全部列和操作按钮，员工自助页用同一个
// 组件，只是把操作列关了、接口换成公开的那一个。
// 加信息时只改这里，两个页面自动同步；加按钮时记得想清楚它是不是只该给管理员。
import { computed } from 'vue'
import EmptyState from './EmptyState.vue'
import StatusBadge from './StatusBadge.vue'
import { formatAgo, formatBytes, formatPercent, formatTime } from '../utils'

export type Row = {
  id: number
  username: string
  display_name: string
  port: number
  address: string
  status: string
  version?: string
  metrics?: { cpu_percent: number; memory_bytes: number }
  disk_bytes?: number
  disk_checked_at?: string
  last_active_at?: string
}

const props = defineProps<{
  items: Row[]
  // 管理员：多一列复选框，名字和「详情」能点进详情页。
  admin?: boolean
  // 游客：点名字看不了详情，改成「打不开？」报障。
  reportable?: boolean
  selected?: number[]
}>()

const emit = defineEmits<{
  (e: 'toggle', id: number): void
  (e: 'toggle-all'): void
  (e: 'report', row: Row): void
}>()

const allChecked = computed(
  () => !!props.selected?.length && props.items.length > 0 && props.items.every((t) => props.selected?.includes(t.id)),
)
</script>

<template>
  <EmptyState v-if="!items.length" title="没有找到租户" hint="换个搜索词，或者创建第一个实例。" />
  <div v-else class="tenant-table">
    <table class="table">
      <thead>
        <tr>
          <th v-if="admin" style="width: 36px">
            <input class="checkbox" type="checkbox" :checked="allChecked" @change="emit('toggle-all')" />
          </th>
          <th>租户</th>
          <th>访问地址</th>
          <th>状态</th>
          <th>版本</th>
          <th>CPU</th>
          <th>内存</th>
          <th>磁盘</th>
          <th>最近活跃</th>
          <th v-if="admin" style="text-align: right">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="t in items" :key="t.id">
          <td v-if="admin">
            <input
              class="checkbox"
              type="checkbox"
              :checked="(selected || []).includes(t.id)"
              @change="emit('toggle', t.id)"
            />
          </td>
          <td>
            <router-link v-if="admin" :to="`/tenants/${t.id}`" class="mono">{{ t.username }}</router-link>
            <span v-else class="mono">{{ t.display_name || t.username }}</span>
            <div class="small muted-2">
              <template v-if="reportable">
                登录名 <span class="mono">{{ t.username }}</span> · 端口 <span class="mono">{{ t.port }}</span>
                <button class="btn btn-ghost btn-sm" type="button" @click="emit('report', t)">打不开？</button>
              </template>
              <template v-else>{{ t.display_name || '—' }}</template>
            </div>
          </td>
          <td><a class="mono small" :href="t.address" target="_blank" rel="noreferrer">{{ t.address }}</a></td>
          <td><StatusBadge :status="t.status" /></td>
          <td class="mono small">{{ t.version || '—' }}</td>
          <td class="num small">{{ formatPercent(t.metrics?.cpu_percent || 0) }}</td>
          <td class="num small">{{ formatBytes(t.metrics?.memory_bytes || 0) }}</td>
          <td class="num small">{{ t.disk_checked_at ? formatBytes(t.disk_bytes || 0) : '未统计' }}</td>
          <td class="small" :title="'最近一次对话：' + formatTime(t.last_active_at || '')">{{ formatAgo(t.last_active_at || '') }}</td>
          <td v-if="admin">
            <div class="actions">
              <slot name="actions" :row="t" />
              <router-link class="btn btn-sm" :to="`/tenants/${t.id}`">详情</router-link>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
