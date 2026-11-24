import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiFetch, apiFetchBlob, ApiRequestError, setCsrfToken } from './client'

describe('api client', () => {
  const originalFetch = globalThis.fetch

  beforeEach(() => {
    setCsrfToken('')
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  it('returns data on valid JSON success envelope and injects CSRF on mutations', async () => {
    setCsrfToken('csrf-secret')
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ data: { ok: true }, request_id: 'req-1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
      }),
    )
    globalThis.fetch = fetchMock

    const result = await apiFetch<{ ok: boolean }>('/api/v1/test', {
      method: 'POST',
      headers: new Headers({ 'X-Custom': 'yes' }),
      body: JSON.stringify({ name: 'a' }),
    })

    expect(result).toEqual({ ok: true })
    const [, init] = fetchMock.mock.calls[0]
    const sentHeaders = init?.headers as Headers
    expect(sentHeaders.get('X-CSRF-Token')).toBe('csrf-secret')
    expect(sentHeaders.get('Content-Type')).toBe('application/json')
    expect(sentHeaders.get('Accept')).toBe('application/json')
    expect(sentHeaders.get('X-Custom')).toBe('yes')
  })

  it('does not attach CSRF token on GET requests', async () => {
    setCsrfToken('csrf-secret')
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ data: 42, request_id: 'req-2' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    globalThis.fetch = fetchMock

    await expect(apiFetch<number>('/api/v1/health')).resolves.toBe(42)
    const sentHeaders = fetchMock.mock.calls[0][1]?.headers as Headers
    expect(sentHeaders.has('X-CSRF-Token')).toBe(false)
  })

  it('throws ApiRequestError instead of SyntaxError on 502 HTML gateway response', async () => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response('<html><body>502 Bad Gateway</body></html>', {
        status: 502,
        headers: { 'Content-Type': 'text/html' },
      }),
    )

    await expect(apiFetch('/api/v1/files')).rejects.toMatchObject({
      name: 'Error',
      code: 'INTERNAL_ERROR',
      message: '服务暂时不可用（HTTP 502）',
    })
    await expect(apiFetch('/api/v1/files')).rejects.toBeInstanceOf(ApiRequestError)
  })

  it('throws ApiRequestError on empty or malformed JSON error response', async () => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response('', {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    await expect(apiFetch('/api/v1/missing')).rejects.toMatchObject({
      code: 'INTERNAL_ERROR',
      message: '请求失败（HTTP 404）',
    })
  })

  it('parses structured JSON error envelope on non-2xx responses', async () => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: 'FORBIDDEN', message: '无权访问', details: { path: '/secret' } },
          request_id: 'req-403',
        }),
        { status: 403, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    await expect(apiFetch('/api/v1/secret')).rejects.toMatchObject({
      code: 'FORBIDDEN',
      message: '无权访问',
      requestId: 'req-403',
      details: { path: '/secret' },
    })
  })

  it('throws ApiRequestError when 200 OK returns unexpected HTML instead of JSON', async () => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response('<!doctype html><html></html>', {
        status: 200,
        headers: { 'Content-Type': 'text/html' },
      }),
    )

    await expect(apiFetch('/api/v1/health')).rejects.toMatchObject({
      code: 'INVALID_RESPONSE',
    })
  })

  it('apiFetchBlob maps non-JSON error responses to ApiRequestError', async () => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response('Gateway Timeout', {
        status: 504,
        headers: { 'Content-Type': 'text/plain' },
      }),
    )

    await expect(apiFetchBlob('/api/v1/download')).rejects.toMatchObject({
      code: 'INTERNAL_ERROR',
      message: '服务暂时不可用（HTTP 504）',
    })
  })
})
