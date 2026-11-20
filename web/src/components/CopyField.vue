<script setup lang="ts">
import { ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'

const props = defineProps<{ value: string; label?: string }>()
const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(props.value)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = props.value
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
  copied.value = true
  window.setTimeout(() => (copied.value = false), 1500)
}
</script>

<template>
  <div>
    <div v-if="label" class="small muted-2" style="margin-bottom: 5px">{{ label }}</div>
    <div class="copy-field">
      <code>{{ value }}</code>
      <button class="btn btn-ghost btn-sm btn-icon" type="button" :title="copied ? '已复制' : '复制'" @click="copy">
        <Check v-if="copied" :size="15" color="var(--green)" />
        <Copy v-else :size="15" />
      </button>
    </div>
  </div>
</template>
