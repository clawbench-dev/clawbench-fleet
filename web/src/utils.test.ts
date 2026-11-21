import { describe, expect, it } from 'vitest'
import { formatAgo, formatBytes, formatPercent, shortSha, statusLabel, actionLabel } from './utils'

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

describe('formatAgo', () => {
  const now = new Date('2026-11-21T12:00:00Z')

  it('says 无记录 when the value is empty', () => {
    expect(formatAgo('', now)).toBe('无记录')
  })

  it('counts minutes, hours and days', () => {
    expect(formatAgo('2026-11-21T11:59:40Z', now)).toBe('刚刚')
    expect(formatAgo('2026-11-21T11:30:00Z', now)).toBe('30 分钟前')
    expect(formatAgo('2026-11-21T09:00:00Z', now)).toBe('3 小时前')
    expect(formatAgo('2026-11-18T12:00:00Z', now)).toBe('3 天前')
  })

  it('falls back to a date past 30 days', () => {
    expect(formatAgo('2026-01-02T12:00:00Z', now)).toBe('2026/1/2')
  })
})
