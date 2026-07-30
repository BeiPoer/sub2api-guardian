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

  const monitoringEnabled = computed(() => status.value?.monitoring_enabled ?? false)
  const configured = computed(() => status.value?.configured ?? false)
  const autoEnabled = computed(() => status.value?.auto_enabled ?? false)

  async function refresh(options: { silent?: boolean } = {}) {
    if (!options.silent) loading.value = true
    try {
      const [overviewData, channelData] = await Promise.all([
        api.overview(),
        api.channels()
      ])
      overview.value = overviewData
      groups.value = overviewData.groups
      status.value = overviewData.status
      channels.value = channelData.items
      error.value = ''
    } catch (err) {
      error.value = (err as Error).message
    } finally {
      loading.value = false
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
    busy.value = true
    try {
      const result = await action()
      error.value = ''
      return result
    } catch (err) {
      error.value = (err as Error).message
      throw err
    } finally {
      busy.value = false
      // 刷新不阻塞调用方：写操作已经结束，没必要让弹窗多等一轮网络往返。
      void refresh({ silent: true })
    }
  }

  function connect() {
    if (source) return
    try {
      source = new EventSource('/api/stream')
      source.onopen = () => {
        connected.value = true
      }
      source.onmessage = () => {
        void refresh({ silent: true })
      }
      source.addEventListener('tick', () => {
        void refresh({ silent: true })
      })
      source.addEventListener('status', event => {
        applyStatusEvent(event as MessageEvent)
      })
      source.addEventListener('ping', event => {
        applyStatusEvent(event as MessageEvent)
      })
      source.onerror = () => {
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
    overview.value = null
    groups.value = []
    channels.value = []
    policy.value = null
    defaults.value = null
    connection.value = null
    status.value = null
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
