// 后端统一响应格式的 API 客户端（README §19）。
// 网页登录态写操作自动携带 X-CSRF-Token（README §8.5）。

export interface ApiError {
  code: string
  message: string
  details?: unknown
}

export class ApiRequestError extends Error {
  code: string
  requestId: string
  details?: unknown

  constructor(error: ApiError, requestId: string) {
    super(error.message)
    this.code = error.code
    this.requestId = requestId
    this.details = error.details
  }
}

interface SuccessEnvelope<T> {
  data: T
  request_id: string
}

interface ErrorEnvelope {
  error: ApiError
  request_id: string
}

// CSRF Token 保存在内存中，登录或 /auth/me 时更新。
let csrfToken = ''

export function setCsrfToken(token: string) {
  csrfToken = token
}

function buildHeaders(defaultAccept: string, method: string, init?: RequestInit): Headers {
  const headers = new Headers(init?.headers)
  if (!headers.has('Accept')) {
    headers.set('Accept', defaultAccept)
  }
  const upperMethod = method.toUpperCase()
  if (upperMethod !== 'GET' && upperMethod !== 'HEAD' && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }
  if (typeof init?.body === 'string' && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  return headers
}

function isJsonResponse(res: Response): boolean {
  const contentType = res.headers.get('Content-Type') ?? ''
  return /(?:^|[\s;])application\/(?:[a-z0-9.+-]+\+)?json(?:$|[\s;])/i.test(contentType)
}

async function parseErrorResponse(res: Response): Promise<ApiRequestError> {
  const fallbackMessage = res.status >= 500
    ? `服务暂时不可用（HTTP ${res.status}）`
    : res.status > 0
      ? `请求失败（HTTP ${res.status}）`
      : '请求失败'
  let error: ApiError = { code: 'INTERNAL_ERROR', message: fallbackMessage }
  let requestId = ''
  if (isJsonResponse(res)) {
    try {
      const parsed = (await res.json()) as Partial<ErrorEnvelope> | null
      if (parsed && typeof parsed === 'object' && parsed.error && typeof parsed.error === 'object') {
        error = {
          code: typeof parsed.error.code === 'string' && parsed.error.code ? parsed.error.code : error.code,
          message: typeof parsed.error.message === 'string' && parsed.error.message ? parsed.error.message : error.message,
          details: parsed.error.details,
        }
        requestId = typeof parsed.request_id === 'string' ? parsed.request_id : ''
      }
    } catch {
      // 响应体为空或不是合法 JSON 时回退到通用错误。
    }
  }
  return new ApiRequestError(error, requestId)
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const method = init?.method ?? 'GET'
  const headers = buildHeaders('application/json', method, init)

  const res = await fetch(path, { ...init, method, headers })
  if (!res.ok) {
    throw await parseErrorResponse(res)
  }
  if (!isJsonResponse(res)) {
    throw new ApiRequestError(
      { code: 'INVALID_RESPONSE', message: '服务器返回了非预期的响应格式' },
      '',
    )
  }
  let body: SuccessEnvelope<T> | ErrorEnvelope
  try {
    body = (await res.json()) as SuccessEnvelope<T> | ErrorEnvelope
  } catch {
    throw new ApiRequestError(
      { code: 'INVALID_RESPONSE', message: '服务器返回了无法解析的 JSON 响应' },
      '',
    )
  }
  if (!body || typeof body !== 'object') {
    throw new ApiRequestError(
      { code: 'INVALID_RESPONSE', message: '服务器返回了非预期的响应格式' },
      '',
    )
  }
  if ('error' in body && body.error) {
    throw new ApiRequestError(body.error, body.request_id ?? '')
  }
  return (body as SuccessEnvelope<T>).data
}

export async function apiFetchBlob(path: string, init?: RequestInit): Promise<Response> {
  const method = init?.method ?? 'GET'
  const headers = buildHeaders('application/octet-stream', method, init)
  const response = await fetch(path, { ...init, method, headers })
  if (!response.ok) {
    throw await parseErrorResponse(response)
  }
  return response
}
