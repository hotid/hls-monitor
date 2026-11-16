// Thin client for the server API. Every call either returns data or throws
// an ApiError with a message that can be shown to the user.

import { perLocale, t } from '@/i18n'

/**
 * URL prefix the application is published under ("" or "/hls-monitor"),
 * taken from the <base> element the server puts into the page.
 */
export const basePath = new URL(document.baseURI).pathname.replace(/\/+$/, '')

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message)
  }
}

let onUnauthorized: (() => void) | null = null

/** Called when a request finds the session gone. */
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

// The server answers in English; these are the messages a user can act on.
const messages = perLocale((): Record<string, string> => ({
  bad_credentials: t('errors.wrongUsernameOrPassword'),
  too_many_attempts: t('errors.tooManyFailedAttemptsTryAgain'),
  unauthorized: t('errors.youNeedToSignInAgain'),
  forbidden: t('errors.notEnoughRightsTheAdministratorRole'),
  not_found: t('errors.notFound'),
  conflict: t('errors.thisNameIsAlreadyTaken'),
  metrics_unavailable: t('common.metricsStorageIsUnavailable'),
  diagnose_busy: t('errors.threeDiagnosesAreRunningNowNo'),
  not_local: t('errors.thisStreamIsCheckedOnlyBy'),
  internal: t('errors.internalServerError'),
}))

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let resp: Response
  try {
    resp = await fetch(basePath + path, {
      method,
      signal,
      credentials: 'same-origin',
      headers: {
        'X-Requested-With': 'hls-monitor',
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError(t('errors.serverIsUnavailable'), 0, 'network')
  }
  if (resp.status === 204 || resp.status === 202) return undefined as T
  let data: unknown = null
  try {
    data = await resp.json()
  } catch {
    /* not JSON */
  }
  if (!resp.ok) {
    const err = (data ?? {}) as { error?: string; code?: string }
    const code = err.code ?? 'error'
    if (resp.status === 401 && code === 'unauthorized' && onUnauthorized) onUnauthorized()
    // Validation messages are specific, so they are shown as the server wrote them.
    const text = code === 'bad_request' || (code === 'conflict' && err.error && !err.error.includes('name')) ? (err.error ?? '') : (messages()[code] ?? err.error ?? '')
    throw new ApiError(text || t('errors.error', { status: resp.status }), resp.status, code)
  }
  return data as T
}

/**
 * Reads a response the server writes line by line, each line a JSON value,
 * and hands every value over as it arrives. Resolves when the server ends
 * the response; aborting the signal ends it from this side.
 */
export async function stream<T>(path: string, signal: AbortSignal, onValue: (v: T) => void): Promise<void> {
  let resp: Response
  try {
    resp = await fetch(basePath + path, { signal, credentials: 'same-origin', headers: { 'X-Requested-With': 'hls-monitor' } })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiError(t('errors.serverIsUnavailable'), 0, 'network')
  }
  if (!resp.ok || !resp.body) {
    const err = ((await resp.json().catch(() => null)) ?? {}) as { error?: string; code?: string }
    const code = err.code ?? 'error'
    if (resp.status === 401 && code === 'unauthorized' && onUnauthorized) onUnauthorized()
    throw new ApiError((code === 'bad_request' ? err.error : messages()[code]) || t('errors.error', { status: resp.status }), resp.status, code)
  }
  const reader = resp.body.getReader()
  const text = new TextDecoder()
  let rest = ''
  for (;;) {
    let chunk: ReadableStreamReadResult<Uint8Array>
    try {
      chunk = await reader.read()
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') throw e
      throw new ApiError(t('errors.connectionToTheServerWasLost'), 0, 'network')
    }
    if (chunk.done) return
    rest += text.decode(chunk.value, { stream: true })
    const lines = rest.split('\n')
    rest = lines.pop() ?? ''
    for (const line of lines) if (line.trim()) onValue(JSON.parse(line) as T)
  }
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, signal),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

export function query(params: Record<string, string | number | boolean | undefined | null>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '' && v !== 0) q.set(k, String(v))
  }
  const s = q.toString()
  return s ? '?' + s : ''
}
