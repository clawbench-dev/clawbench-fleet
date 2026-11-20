import { describe, expect, it } from 'vitest'
import { formatBytes, formatPercent, shortSha, statusLabel, actionLabel } from './utils'

describe('formatBytes', () => {
  it('renders plain bytes', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB')
    expect(formatBytes(1.5 * 1024 * 1024 * 1024)).toBe('1.5 GB')
  })
  it('clamps negative values', () => {
    expect(formatBytes(-5)).toBe('0 B')
  })
})

describe('formatPercent', () => {
  it('formats percents', () => {
    expect(formatPercent(12.34)).toBe('12%')
    expect(formatPercent(0.5)).toBe('0.5%')
    expect(formatPercent(NaN)).toBe('0%')
  })
})

describe('labels', () => {
  it('maps status labels', () => {
    expect(statusLabel('running')).toBe('运行中')
    expect(statusLabel('unknown')).toBe('unknown')
  })
  it('maps action labels', () => {
    expect(actionLabel('tenant.restart')).toBe('重启')
    expect(actionLabel('unknown.action')).toBe('unknown.action')
  })
  it('shortens sha256', () => {
    expect(shortSha('abc')).toBe('abc')
    expect(shortSha('a'.repeat(64)).length).toBe(12)
    expect(shortSha('')).toBe('—')
  })
})
