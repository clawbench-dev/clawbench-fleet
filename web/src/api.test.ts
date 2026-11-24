import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, downloadFile, errorMessage } from './api'

type FetchCall = { url: string; init: RequestInit }

let calls: FetchCall[] = []

function mockFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      calls.push({ url, init })
      return handler(url, init)
    }),
  )
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('api', () => {
  beforeEach(() => {
    vi.stubGlobal('window', { dispatchEvent: vi.fn() })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('sends GET requests with credentials', async () => {
    mockFetch(() => json({ items: [] }))
    const res = await api.get<{ items: unknown[] }>('/tenants')
    expect(res.items).toEqual([])
    expect(calls[0].url).toBe('/api/tenants')
    expect(calls[0].init.method).toBe('GET')
    expect(calls[0].init.credentials).toBe('include')
  })

  it('sends JSON bodies for mutating calls', async () => {
    mockFetch(() => json({ ok: true }, 201))
    await api.post('/tenants', { username: 'alice' })
    await api.patch('/tenants/1', { notes: 'x' })
    await api.put('/settings/tenant-config', { entries: [] })
    await api.del('/tenants/1', { confirm: 'alice' })
    const [post, patch, put, del] = calls
    expect(post.init.method).toBe('POST')
    expect(post.init.body).toBe(JSON.stringify({ username: 'alice' }))
    expect((post.init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(patch.init.method).toBe('PATCH')
    expect(put.init.method).toBe('PUT')
    expect(put.init.body).toBe(JSON.stringify({ entries: [] }))
    expect(del.init.method).toBe('DELETE')
  })

  it('sends FormData without a JSON content type', async () => {
    mockFetch(() => json({ ok: true }))
    const form = new FormData()
    form.append('file', new File(['x'], 'a.csv'))
    await api.upload('/tenants/import', new File(['x'], 'a.csv'))
    expect(calls[0].init.body).toBeInstanceOf(FormData)
    expect((calls[0].init.headers as Record<string, string>)['Content-Type']).toBeUndefined()
  })

  it('turns error responses into ApiError', async () => {
    mockFetch(() => json({ error: { code: 'not_found', message: '租户不存在' } }, 404))
    await expect(api.get('/tenants/9')).rejects.toMatchObject({ status: 404, code: 'not_found', message: '租户不存在' })
  })

  it('falls back to a generic message when the error body is missing', async () => {
    mockFetch(() => new Response('oops', { status: 500 }))
    const err = await api.get('/broken').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('http_error')
    expect((err as ApiError).message).toContain('500')
  })

  it('treats an empty response body as no data', async () => {
    mockFetch(() => new Response('', { status: 200 }))
    await expect(api.post('/auth/logout')).resolves.toBeNull()
  })

  it('announces an expired session', async () => {
    mockFetch(() => json({ error: { code: 'unauthorized', message: '请先登录' } }, 401))
    await expect(api.get('/auth/me')).rejects.toBeInstanceOf(ApiError)
    expect(window.dispatchEvent).toHaveBeenCalled()
  })
})

describe('errorMessage', () => {
  it('prefers the error message', () => {
    expect(errorMessage(new Error('租户不存在'))).toBe('租户不存在')
    expect(errorMessage(new ApiError(400, 'bad', '参数不对'), '兜底')).toBe('参数不对')
  })

  it('accepts thrown strings and falls back otherwise', () => {
    expect(errorMessage('网络中断')).toBe('网络中断')
    expect(errorMessage(null, '兜底')).toBe('兜底')
    expect(errorMessage({ weird: true })).toBe('操作失败')
  })
})

describe('downloadFile', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('downloads through a temporary link', async () => {
    mockFetch(() => new Response(new Blob(['data']), { status: 200 }))
    const clicked: string[] = []
    const anchor = {
      href: '',
      download: '',
      click: () => clicked.push('click'),
      remove: () => clicked.push('remove'),
    }
    vi.stubGlobal('URL', { createObjectURL: () => 'blob:x', revokeObjectURL: vi.fn() })
    const append = vi.fn()
    vi.stubGlobal('document', {
      createElement: () => anchor,
      body: { appendChild: append },
    })
    await downloadFile('/backups/1/download', 'alice.tar.gz')
    expect(calls[0].url).toBe('/api/backups/1/download')
    expect(anchor.download).toBe('alice.tar.gz')
    expect(append).toHaveBeenCalledWith(anchor)
    expect(clicked).toEqual(['click', 'remove'])
  })

  it('throws when the download fails', async () => {
    mockFetch(() => new Response('', { status: 404 }))
    await expect(downloadFile('/backups/9/download', 'x')).rejects.toBeInstanceOf(ApiError)
  })
})
