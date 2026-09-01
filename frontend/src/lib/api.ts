import type {
  Action,
  Channel,
  Connection,
  Group,
  GuardianEvent,
  Image2Config,
  Image2Settings,
  Image2Upstream,
  Memo,
  MemoArchive,
  MemoSummary,
  MemoType,
  Overview,
  Policy,
  UpstreamAlert,
  UpstreamAutomationTask,
  UpstreamBalanceLog,
  UpstreamChannel,
  UpstreamEmailSettings,
  UpstreamWeComSettings,
  UpstreamOverview,
  UpstreamTokenModels,
  ChannelUsageReportRun,
  ChannelUsageReportSaveInput,
  ChannelUsageReportView,
  DailyReportRun,
  DailyReportSaveInput,
  DailyReportView,
  ReportNotificationConfig,
  ReportNotificationSaveInput,
  ReportSourceConfig,
  ReportSourceSaveInput
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

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string
  ) {
    super(message)
    this.name = 'ApiError'
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
      throw new Error(`请求超时（${Math.round(timeoutMs / 1000)} 秒），请检查上游服务是否可达`)
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
    const error = payload as { error?: string; code?: string }
    const message = error.error || response.statusText
    throw new ApiError(message || `请求 ${path} 失败`, response.status, error.code)
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
  },

  image2: () => request<Image2Config>('/api/image2'),
  saveImage2Settings: (payload: {
    image_domain: string
    retention_hours: number
    proxy_api_key?: string
  }) => put<Image2Settings>('/api/image2/settings', payload),
  createImage2Upstream: (payload: Record<string, unknown>) =>
    post<Image2Upstream>('/api/image2/upstreams', payload),
  updateImage2Upstream: (id: number, payload: Record<string, unknown>) =>
    put<Image2Upstream>(`/api/image2/upstreams/${id}`, payload),
  deleteImage2Upstream: (id: number) =>
    request<{ ok: boolean }>(`/api/image2/upstreams/${id}`, { method: 'DELETE' }),

  memos: () => request<{ items: MemoSummary[] }>('/api/memos'),
  memo: (id: number) => request<Memo>(`/api/memos/${id}`),
  createMemo: (payload: { title: string; type: MemoType }) => post<Memo>('/api/memos', payload),
  updateMemo: (
    id: number,
    payload: {
      title: string
      content: Memo['content']
      expected_revision: number
      force?: boolean
    }
  ) => put<Memo>(`/api/memos/${id}`, payload),
  deleteMemo: (id: number, expectedRevision: number, force = false) =>
    request<{ ok: boolean }>(`/api/memos/${id}`, {
      method: 'DELETE',
      body: JSON.stringify({ expected_revision: expectedRevision, force })
    }),
  memoArchives: (id: number) =>
    request<{ items: MemoArchive[] }>(`/api/memos/${id}/archives`),
  restoreMemoArchive: (id: number, archiveID: number, expectedRevision: number, force = false) =>
    post<Memo>(`/api/memos/${id}/archives/${archiveID}/restore`, {
      expected_revision: expectedRevision,
      force
    }),

  upstreamChannels: () =>
    request<{ items: UpstreamChannel[]; total: number; active: number; ignored: number }>(
      '/api/upstream-channels'
    ),
  createUpstreamChannel: (payload: Record<string, unknown>) =>
    post<UpstreamChannel>('/api/upstream-channels', payload, LONG_TIMEOUT_MS),
  updateUpstreamChannel: (id: number, payload: Record<string, unknown>) =>
    put<UpstreamChannel>(`/api/upstream-channels/${id}`, payload, LONG_TIMEOUT_MS),
  deleteUpstreamChannel: (id: number) =>
    request<{ ok: boolean }>(`/api/upstream-channels/${id}`, { method: 'DELETE' }),
  syncUpstreamChannel: (id: number) =>
    post<{ ok: boolean; channel: UpstreamChannel }>(
      `/api/upstream-channels/${id}/sync`,
      undefined,
      LONG_TIMEOUT_MS
    ),
  syncAllUpstreamChannels: () =>
    post<{ ok: boolean; synced: number; failed: number }>(
      '/api/upstream-channels/sync',
      undefined,
      LONG_TIMEOUT_MS
    ),
  upstreamOverview: (id: number) =>
    request<UpstreamOverview>(`/api/upstream-channels/${id}/overview`),
  upstreamTokenModels: (channelID: number, tokenID: number) =>
    request<UpstreamTokenModels>(
      `/api/upstream-channels/${channelID}/tokens/${tokenID}/models`,
      undefined,
      LONG_TIMEOUT_MS
    ),
  updateUpstreamTokenGroup: (channelID: number, tokenID: number, payload: unknown) =>
    put<{ token: unknown; tokens: unknown[] }>(
      `/api/upstream-channels/${channelID}/tokens/${tokenID}/group`,
      payload,
      LONG_TIMEOUT_MS
    ),
  upstreamTasks: (channelID: number) =>
    request<{ items: UpstreamAutomationTask[] }>(
      `/api/upstream-channels/${channelID}/tasks`
    ),
  createUpstreamTask: (channelID: number, payload: Record<string, unknown>) =>
    post<UpstreamAutomationTask>(`/api/upstream-channels/${channelID}/tasks`, payload),
  updateUpstreamTask: (channelID: number, taskID: number, payload: Record<string, unknown>) =>
    put<UpstreamAutomationTask>(
      `/api/upstream-channels/${channelID}/tasks/${taskID}`,
      payload
    ),
  deleteUpstreamTask: (channelID: number, taskID: number) =>
    request<{ ok: boolean }>(
      `/api/upstream-channels/${channelID}/tasks/${taskID}`,
      { method: 'DELETE' }
    ),
  upstreamBalanceLogs: (channelID: number, page = 1) =>
    request<{
      items: UpstreamBalanceLog[]
      total: number
      page: number
      page_size: number
      pages: number
    }>(`/api/upstream-channels/${channelID}/balance-logs?page=${page}`),
  upstreamAlerts: (channelID: number) =>
    request<{ items: UpstreamAlert[] }>(`/api/upstream-channels/${channelID}/alerts`),
  upstreamEmailSettings: () =>
    request<UpstreamEmailSettings>('/api/upstream-email-settings'),
  saveUpstreamEmailSettings: (payload: Record<string, unknown>) =>
    put<UpstreamEmailSettings>('/api/upstream-email-settings', payload),
  testUpstreamEmailSettings: (recipients: string[]) =>
    post<{ ok: boolean; message_id: string }>('/api/upstream-email-settings/test', {
      recipients
    }),
  upstreamWeComSettings: () =>
    request<UpstreamWeComSettings>('/api/upstream-wecom-settings'),
  saveUpstreamWeComSettings: (payload: Record<string, unknown>) =>
    put<UpstreamWeComSettings>('/api/upstream-wecom-settings', payload),
  testUpstreamWeComSettings: (target?: string) =>
    post<{ ok: boolean; message_id: string }>('/api/upstream-wecom-settings/test', {
      target: target || ''
    }),

  reportNotificationSettings: () =>
    request<ReportNotificationConfig>('/api/reports/notifications'),
  saveReportNotificationSettings: (payload: ReportNotificationSaveInput) =>
    put<ReportNotificationConfig>('/api/reports/notifications', payload),
  testReportNotificationWeCom: () =>
    post<{ ok: boolean; message_id: string }>('/api/reports/notifications/wecom/test', undefined, LONG_TIMEOUT_MS),

  reportSourceSettings: () =>
    request<ReportSourceConfig>('/api/reports/source'),
  saveReportSourceSettings: (payload: ReportSourceSaveInput) =>
    put<ReportSourceConfig>('/api/reports/source', payload),

  channelUsageReport: () =>
    request<ChannelUsageReportView>('/api/reports/channel-usage'),
  saveChannelUsageReport: (payload: ChannelUsageReportSaveInput) =>
    put<ChannelUsageReportView>('/api/reports/channel-usage', payload),
  channelUsageReportRuns: (page = 1, pageSize = 20) =>
    request<{
      items: ChannelUsageReportRun[]
      total: number
      page: number
      page_size: number
      pages: number
    }>(`/api/reports/channel-usage/runs?page=${page}&page_size=${pageSize}`),
  runChannelUsageReport: () =>
    post<{ run: ChannelUsageReportRun }>('/api/reports/channel-usage/run', undefined, LONG_TIMEOUT_MS),

  dailyReport: () =>
    request<DailyReportView>('/api/reports/daily'),
  saveDailyReport: (payload: DailyReportSaveInput) =>
    put<DailyReportView>('/api/reports/daily', payload),
  dailyReportRuns: (page = 1, pageSize = 20) =>
    request<{
      items: DailyReportRun[]
      total: number
      page: number
      page_size: number
      pages: number
    }>(`/api/reports/daily/runs?page=${page}&page_size=${pageSize}`),
  runDailyReport: () =>
    post<{ run: DailyReportRun }>('/api/reports/daily/run', undefined, LONG_TIMEOUT_MS)
}
