<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import Modal from './Modal.vue'

const props = defineProps<{
  open: boolean
  title: string
  message: string
  confirmText?: string
  danger?: boolean
  requireText?: string
}>()

const emit = defineEmits<{ confirm: []; cancel: [] }>()
const typed = ref('')

watch(
  () => props.open,
  (open) => {
    if (open) typed.value = ''
  },
)

const canConfirm = computed(() => {
  if (!props.requireText) return true
  return typed.value.trim() === props.requireText
})
</script>

<template>
  <Modal v-if="open" :title="title" @close="emit('cancel')">
    <p class="muted" style="white-space: pre-wrap">{{ message }}</p>
    <div v-if="requireText" class="field" style="margin-top: 16px">
      <label>请输入 <span class="mono">{{ requireText }}</span> 以确认</label>
      <input v-model="typed" class="input" autocomplete="off" />
    </div>
    <template #footer>
      <button class="btn" type="button" @click="emit('cancel')">取消</button>
      <button
        class="btn"
        :class="danger ? 'btn-danger' : 'btn-primary'"
        type="button"
        :disabled="!canConfirm"
        @click="emit('confirm')"
      >
        {{ confirmText || '确认' }}
      </button>
    </template>
  </Modal>
</template>
