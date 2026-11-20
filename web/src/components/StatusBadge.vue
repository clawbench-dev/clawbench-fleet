<script setup lang="ts">
import { computed } from 'vue'
import { statusLabel } from '../utils'

const props = defineProps<{ status: string }>()
const known = ['running', 'stopped', 'error', 'creating', 'deleting', 'trashed']
const cls = computed(() => `badge badge-${known.includes(props.status) ? props.status : 'neutral'}`)
</script>

<template>
  <span :class="cls">{{ statusLabel(status) }}</span>
</template>
