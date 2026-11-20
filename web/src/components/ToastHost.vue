<script setup lang="ts">
import { AlertCircle, CheckCircle2, Info } from 'lucide-vue-next'
import { dismiss, toasts } from '../stores/toast'
</script>

<template>
  <div class="toast-host">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="`toast-${t.type}`" @click="dismiss(t.id)">
      <CheckCircle2 v-if="t.type === 'success'" :size="16" color="var(--green)" />
      <AlertCircle v-else-if="t.type === 'error'" :size="16" color="var(--red)" />
      <Info v-else :size="16" color="var(--accent)" />
      <span>{{ t.message }}</span>
    </div>
  </div>
</template>
