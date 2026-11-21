import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { dismiss, toast, toasts } from './toast'

describe('toast store', () => {
  beforeEach(() => {
    toasts.splice(0, toasts.length)
    vi.useFakeTimers()
    vi.stubGlobal('window', { setTimeout })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('queues a toast and dismisses it after the delay', () => {
    toast('已保存', 'success', 1000)
    expect(toasts).toHaveLength(1)
    expect(toasts[0].message).toBe('已保存')
    expect(toasts[0].type).toBe('success')
    vi.advanceTimersByTime(999)
    expect(toasts).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(toasts).toHaveLength(0)
  })

  it('defaults to an info toast', () => {
    toast('提示')
    expect(toasts[0].type).toBe('info')
  })

  it('ignores a dismiss for an unknown id', () => {
    toast('a')
    dismiss(-1)
    expect(toasts).toHaveLength(1)
  })
})
