import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '@/lib/api'
import type { Channel, Connection, EngineStatus, Group, Overview, Policy } from '@/lib/types'

/** 轮询兜底间隔：SSE 正常时基本用不到，断线时保证数据仍会刷新。 */
const POLL_INTERVAL_MS = 20_000

export const useGuardianStore = defineStore('guardian', () => {
  const overview = ref<Overview | null>(null)
  const groups = ref<Group[]>([])
  const channels = ref<Channel[]>([])
  const policy = ref<Policy | null>(null)
  const defaults = ref<Policy | null>(null)
  const connection = ref<Connection | null>(null)
  const status = ref<EngineStatus | null>(null)

  const loading = ref(false)
  const busy = ref(false)
  const error = ref('')
  const connected = ref(false)

  let source: EventSource | null = null
  let poller: number | null = null

  // 初始加载、SSE 与轮询可能同时要求刷新。单飞 + 一次尾随刷新可以避免旧响应
  // 覆盖新数据，也不会在一轮调度结束时瞬间打出多组重复请求。
  let refreshTask: Promise<void> | null = null
  let refreshQueued = false
  let dataEpoch = 0
  let statusRevision = 0

  const monitoringEnabled = computed(() => status.value?.monitoring_enabled ?? false)
  const configured = computed(() => status.value?.configured ?? false)
  const autoEnabled = computed(() => status.value?.auto_enabled ?? false)

  async function refresh(options: { silent?: boolean } = {}) {
    if (!options.silent) loading.value = true

    if (refreshTask) {
      refreshQueued = true
      return refreshTask
    }

    const epoch = dataEpoch
    const task = (async () => {
      // 一轮请求期间无论收到多少通知，都只额外补跑一次。
      for (let pass = 0; pass < 2; pass++) {
        refreshQueued = false
        const revisionAtStart = statusRevision
        const failures: string[] = []

        // 两个请求独立落地：渠道列表再大，也不会阻塞 status 与概览先显示。
        const overviewRequest = api
          .overview()
          .then(data => {
            if (dataEpoch !== epoch) return
            overview.value = data
            groups.value = data.groups
            // 请求期间若收到更新的 SSE 状态，不用较旧的 HTTP 快照覆盖它。
            if (statusRevision === revisionAtStart) status.value = data.status
          })
          .catch(err => failures.push((err as Error).message))

        const channelsRequest = api
          .channels()
          .then(data => {
            if (dataEpoch === epoch) channels.value = data.items
          })
          .catch(err => failures.push((err as Error).message))

        await Promise.all([overviewRequest, channelsRequest])
        if (dataEpoch !== epoch) return

        error.value = failures.length > 0 ? Array.from(new Set(failures)).join('；') : ''
        if (!refreshQueued || dataEpoch !== epoch) break
      }
    })()

    refreshTask = task
    try {
      await task
    } finally {
      // reset 后可能已有新登录周期的刷新，不允许旧任务清掉新任务的加载态。
      if (refreshTask === task) {
        refreshTask = null
        if (dataEpoch === epoch) loading.value = false
      }
    }
  }

  async function loadPolicy() {
    const data = await api.policy()
    policy.value = data.policy
    defaults.value = data.defaults
    return data.policy
  }

  async function loadConnection() {
    connection.value = await api.connection()
    return connection.value
  }

  /** run 包装所有写操作：统一处理 busy 状态、错误提示和刷新。 */
  async function run<T>(action: () => Promise<T>): Promise<T> {
    const epoch = dataEpoch
    busy.value = true
    try {
      const result = await action()
      if (dataEpoch === epoch) error.value = ''
      return result
    } catch (err) {
      if (dataEpoch === epoch) error.value = (err as Error).message
      throw err
    } finally {
      if (dataEpoch === epoch) {
        busy.value = false
        // 刷新不阻塞调用方：写操作已经结束，没必要让弹窗多等一轮网络往返。
        void refresh({ silent: true })
      }
    }
  }

  function connect() {
    if (source) return
    const connectionEpoch = dataEpoch
    try {
      source = new EventSource('/api/stream')
      source.onopen = () => {
        if (dataEpoch !== connectionEpoch) return
        connected.value = true
      }
      source.onmessage = () => {
        if (dataEpoch !== connectionEpoch) return
        void refresh({ silent: true })
      }
      source.addEventListener('tick', () => {
        if (dataEpoch !== connectionEpoch) return
        void refresh({ silent: true })
      })
      source.addEventListener('status', event => {
        if (dataEpoch === connectionEpoch) applyStatusEvent(event as MessageEvent)
      })
      source.addEventListener('ping', event => {
        if (dataEpoch === connectionEpoch) applyStatusEvent(event as MessageEvent)
      })
      source.onerror = () => {
        if (dataEpoch !== connectionEpoch) return
        connected.value = false
      }
    } catch {
      connected.value = false
    }

    if (poller === null) {
      poller = window.setInterval(() => void refresh({ silent: true }), POLL_INTERVAL_MS)
    }
  }

  function applyStatusEvent(event: MessageEvent) {
    try {
      status.value = JSON.parse(event.data) as EngineStatus
      statusRevision++
      connected.value = true
    } catch {
      // 忽略非法负载，等待下一次事件。
    }
  }

  function disconnect() {
    source?.close()
    source = null
    connected.value = false
    if (poller !== null) {
      window.clearInterval(poller)
      poller = null
    }
  }

  /**
   * reset 清空内存里的业务数据，退出登录时调用。
   *
   * 不清的话，下一个登录进来的人会在数据加载完成前先看到上一个人的渠道列表。
   */
  function reset() {
    dataEpoch++
    statusRevision = 0
    refreshQueued = false
    refreshTask = null
    overview.value = null
    groups.value = []
    channels.value = []
    policy.value = null
    defaults.value = null
    connection.value = null
    status.value = null
    loading.value = false
    busy.value = false
    error.value = ''
  }

  return {
    overview,
    groups,
    channels,
    policy,
    defaults,
    connection,
    status,
    loading,
    busy,
    error,
    connected,
    monitoringEnabled,
    configured,
    autoEnabled,
    refresh,
    loadPolicy,
    loadConnection,
    run,
    connect,
    disconnect,
    reset
  }
})
