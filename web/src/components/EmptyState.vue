<script setup lang="ts">
import { Inbox } from 'lucide-vue-next'

defineProps<{ title: string; hint?: string }>()
</script>

<template>
  <div class="empty">
    <Inbox />
    <div style="font-weight: 600; color: var(--muted)">{{ title }}</div>
    <div v-if="hint" class="small" style="margin-top: 4px">{{ hint }}</div>
    <div v-if="$slots.default" style="margin-top: 14px">
      <slot />
    </div>
  </div>
</template>
