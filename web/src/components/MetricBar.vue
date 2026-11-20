<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  label: string
  value: string
  percent: number
  tone?: 'accent' | 'green' | 'amber' | 'red'
}>()

const width = computed(() => `${Math.max(0, Math.min(100, props.percent || 0))}%`)
</script>

<template>
  <div>
    <div class="row-between" style="margin-bottom: 7px">
      <span class="small muted">{{ label }}</span>
      <span class="small mono">{{ value }}</span>
    </div>
    <div class="progress" :class="tone && tone !== 'accent' ? tone : ''">
      <span :style="{ width }" />
    </div>
  </div>
</template>
