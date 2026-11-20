import { reactive } from 'vue'

export type ToastType = 'info' | 'success' | 'error'

export interface Toast {
  id: number
  message: string
  type: ToastType
}

let seq = 0
export const toasts = reactive<Toast[]>([])

export function toast(message: string, type: ToastType = 'info', ms = 4000) {
  const id = ++seq
  toasts.push({ id, message, type })
  window.setTimeout(() => dismiss(id), ms)
}

export function dismiss(id: number) {
  const idx = toasts.findIndex((t) => t.id === id)
  if (idx >= 0) toasts.splice(idx, 1)
}
