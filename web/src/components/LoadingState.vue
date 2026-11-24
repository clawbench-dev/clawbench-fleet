<script setup lang="ts">
import { LoaderCircle } from 'lucide-vue-next'

defineProps<{ text?: string }>()
</script>

<template>
  <div class="loading">
    <LoaderCircle class="spin" />
    <span>{{ text || '加载中…' }}</span>
  </div>
</template>

<style scoped>
.spin {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
