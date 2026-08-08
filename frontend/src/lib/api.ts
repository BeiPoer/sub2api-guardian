import type {
  Action,
  Channel,
  Connection,
  Group,
  GuardianEvent,
  Overview,
  Policy
} from './types'

export interface EventPage {
  items: GuardianEvent[]
  total: number
  page: number
  page_size: number
}

/** 常规请求的超时上限：没有它，上游卡住时按钮会一直转圈。 */
const DEFAULT_TIMEOUT_MS = 30_000

/** 探测与拉模型要真的打一次上游，给更长的时间。 */
const LONG_TIMEOUT_MS = 180_000

/**
 * UnauthorizedError 表示会话失效，需要重新登录。
 *
 * 单独立一个类型是为了让调用方能区分「没登录」和「请求出错」——
 * 前者应该切到登录页，后者才该弹错误提示。
 */
export class UnauthorizedError extends Error {
  constructor(message = '请先登录') {
    super(message)
    this.name = 'UnauthorizedError'
  }
}

/** onUnauthorized 在任意请求遇到 401 时触发，由 auth store 注册。 */
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(handler: (() => void) | null) {
  onUnauthorized = handler
}

/** 后端返回的错误统一转成 Error 抛出，调用方只需 catch 一次。 */
async function request<T>(path: string, init?: RequestInit, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<T> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)

  let response: Response
  try {
    response = await fetch(path, {
      headers: { 'Content-Type': 'application/json' },
      signal: controller.signal,
      // 会话走 HttpOnly Cookie，请求必须带上凭据才认得出登录态。
      credentials: 'same-origin',
      ...init
    })
  } catch (err) {
    if ((err as Error).name === 'AbortError') {
      throw new Error(`请求超时（${Math.round(timeoutMs / 1000)} 秒），请检查 sub2api 是否可达`)
    }
    throw err
  } finally {
    window.clearTimeout(timer)
  }

  const contentType = response.headers.get('content-type') || ''
  const isJson = contentType.includes('application/json')
  const payload = isJson ? await response.json().catch(() => ({})) : {}

  if (response.status === 401) {
    // 会话失效：通知 auth store 切回登录页，不要在各处弹一堆错误提示。
    onUnauthorized?.()
    throw new UnauthorizedError((payload as { error?: string }).error)
  }
  if (!response.ok) {
    const message = (payload as { error?: string }).error || response.statusText
    throw new Error(message || `请求 ${path} 失败`)
  }
  if (!isJson) {
    throw new Error(`接口 ${path} 未返回 JSON，请确认 Guardian 后端已启动`)
  }
  return payload as T
}

function post<T>(path: string, body?: unknown, timeoutMs?: number): Promise<T> {
  return request<T>(
    path,
    { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) },
    timeoutMs
  )
}

function put<T>(path: string, body: unknown, timeoutMs?: number): Promise<T> {
  return request<T>(path, { method: 'PUT', body: JSON.stringify(body) }, timeoutMs)
}

export const api = {
  // —— 鉴权与初始化 ——
  setupStatus: () => request<{ needs_setup: boolean; has_users: boolean }>('/api/setup'),
  setup: (payload: {
    username: string
    password: string
    base_url: string
    admin_api_key: string
    timeout_seconds?: number
  }) => post<{ ok: boolean; username: string; sync_error?: string }>('/api/setup', payload, LONG_TIMEOUT_MS),
  login: (username: string, password: string) =>
    post<{ ok: boolean; username: string }>('/api/login', { username, password }),
  logout: () => post<{ ok: boolean }>('/api/logout'),
  me: () => request<{ username: string; created_at: string }>('/api/me'),
  updateAccount: (payload: {
    current_password: string
    username?: string
    new_password?: string
  }) => put<{ ok: boolean; username: string; changed: string[] }>('/api/account', payload),

  overview: () => request<Overview>('/api/overview'),
  groups: () => request<{ items: Group[] }>('/api/groups'),
  channels: (params: Record<string, string> = {}) => {
    const query = new URLSearchParams(params).toString()
    return request<{ items: Channel[]; total: number }>(
      `/api/channels${query ? `?${query}` : ''}`
    )
  },
  channel: (id: number) =>
    request<{ channel: Channel; actions: Action[] }>(`/api/channels/${id}`),
  policy: () => request<{ policy: Policy; defaults: Policy }>('/api/policy'),
  savePolicy: (policy: Partial<Policy>) => put<{ policy: Policy }>('/api/policy', policy),
  saveGroupPolicy: (id: number, override: unknown) =>
    put<{ ok: boolean }>(`/api/groups/${id}/policy`, override),
  excludeGroup: (id: number, excluded: boolean) =>
    post<{ ok: boolean }>(`/api/groups/${id}/exclude`, { excluded }),
  clearGroupPolicy: (id: number) =>
    request<{ ok: boolean }>(`/api/groups/${id}/policy`, { method: 'DELETE' }),
  connection: () => request<Connection>('/api/connection'),
  saveConnection: (payload: Record<string, unknown>) =>
    put<Connection>('/api/connection', payload),
  // 这几个会真的打上游、可能跑很久，给更长的超时。
  sync: () =>
    post<{
      ok: boolean
      groups?: number
      channels?: number
      available_accounts?: number
      healthy_accounts?: number
      rate_limited_accounts?: number
      total_accounts?: number
    }>('/api/sync', undefined, LONG_TIMEOUT_MS),
  runOnce: () => post<{ ok: boolean }>('/api/run-once', undefined, LONG_TIMEOUT_MS),
  cancelRun: () => post<{ ok: boolean; canceled: boolean }>('/api/cancel'),
  resumeRun: () => post<{ ok: boolean }>('/api/resume'),
  restoreAll: () =>
    post<{ ok: boolean; restored: number }>('/api/restore-all', undefined, LONG_TIMEOUT_MS),
  probeChannel: (id: number) =>
    post<{ ok: boolean }>(`/api/channels/${id}/probe`, undefined, LONG_TIMEOUT_MS),
  fuseChannel: (id: number, reason: string) =>
    post<{ ok: boolean }>(`/api/channels/${id}/fuse`, { reason }),
  recoverChannel: (id: number) => post<{ ok: boolean }>(`/api/channels/${id}/recover`),
  excludeChannel: (id: number, excluded: boolean) =>
    post<{ ok: boolean }>(`/api/channels/${id}/exclude`, { excluded }),
  pauseChannel: (id: number, paused: boolean) =>
    post<{ ok: boolean }>(`/api/channels/${id}/pause`, { paused }),
  updateChannel: (id: number, payload: Record<string, unknown>) =>
    put<{ ok: boolean }>(`/api/channels/${id}`, payload),
  syncUpstreamMultiplier: (id: number) =>
    post<{
      ok: boolean
      multiplier: number
      previous_multiplier: number
      updated_at: string
    }>(`/api/channels/${id}/sync-upstream-multiplier`),
  channelModels: (id: number) =>
    request<{ models: string[] }>(`/api/channels/${id}/models`, undefined, LONG_TIMEOUT_MS),
  setChannelTestModel: (id: number, modelID: string) =>
    put<{ ok: boolean }>(`/api/channels/${id}/test-model`, { model_id: modelID }),
  events: (params: Record<string, string> = {}) => {
    const query = new URLSearchParams(params).toString()
    return request<EventPage>(`/api/events${query ? `?${query}` : ''}`)
  },
  actions: (accountID?: number) => {
    const query = accountID ? `?account_id=${accountID}` : ''
    return request<{ items: Action[] }>(`/api/actions${query}`)
  }
}
