import { beforeEach, describe, expect, it, vi } from 'vitest'

const get = vi.fn()
const post = vi.fn()

vi.mock('../api', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: (...args: unknown[]) => post(...args),
  },
}))

import { auth, loadMe, login, logout } from './auth'

describe('auth store', () => {
  beforeEach(() => {
    auth.admin = null
    auth.ready = false
    get.mockReset()
    post.mockReset()
  })

  it('loads the current admin', async () => {
    get.mockResolvedValue({ admin: { id: 1, username: 'admin' } })
    await loadMe()
    expect(auth.admin?.username).toBe('admin')
    expect(auth.ready).toBe(true)
  })

  it('treats a failed load as logged out', async () => {
    get.mockRejectedValue(new Error('401'))
    await loadMe()
    expect(auth.admin).toBeNull()
    expect(auth.ready).toBe(true)
  })

  it('logs in', async () => {
    post.mockResolvedValue({ admin: { id: 1, username: 'admin' } })
    await login('admin', 'secret')
    expect(post).toHaveBeenCalledWith('/auth/login', { username: 'admin', password: 'secret' })
    expect(auth.admin?.id).toBe(1)
  })

  it('clears the admin on logout even when the call fails', async () => {
    auth.admin = { id: 1, username: 'admin' } as never
    post.mockRejectedValue(new Error('network'))
    await expect(logout()).rejects.toThrow('network')
    expect(auth.admin).toBeNull()
  })
})
