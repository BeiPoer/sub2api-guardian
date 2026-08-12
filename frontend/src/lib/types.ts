// Guardian 后端返回的数据结构，与 backend/internal/api/dto.go 保持一致。

export type Strategy = 'price' | 'speed' | 'balanced'

export interface Image2Settings {
  image_domain: string
  retention_hours: number
  has_proxy_api_key: boolean
}

export interface Image2Upstream {
  id: number
  name: string
  slug: string
  base_url: string
  has_api_key: boolean
  model_mapping: string
  blocked_params: string
}

export interface Image2Config {
  settings: Image2Settings
  upstreams: Image2Upstream[]
}

export type ChannelHealth =
  | 'unknown'
  | 'healthy'
  | 'degraded'
  | 'fused'
  | 'excluded'
  | 'survivor'
  | 'paused'

export type GroupStatus =
  | 'empty'
  | 'skipped'
  | 'healthy'
  | 'partial_degraded'
  /** 组内有渠道正在限流，但没有真故障：会随窗口重置自愈，不用人介入。 */
  | 'rate_limited'
  | 'survivor_only'
  | 'all_fused'

export interface GroupRef {
  id: number
  name: string
}

export interface Sample {
  occurred_at: string
  source: 'probe' | 'traffic'
  event_type: string
  score: number
  ttfb_ms: number
  status_code: number
  message: string
}

export interface Channel {
  id: number
  name: string
  platform: string
  type: string
  status: string
  groups: GroupRef[]
  primary_group_id?: number
  schedulable: boolean
  excluded: boolean
  paused: boolean
  managed: boolean

  /**
   * sub2api 此刻不把请求发给它的原因类别，可用时为空。
   *
   * 这是与网站对账的关键：探测出的健康分再高，只要上游在限流窗口里就一个
   * 请求都接不到。没有它，页面上会出现「健康分 100 却不接流量」的困惑。
   */
  upstream_block?:
    | ''
    | 'disabled'
    | 'unschedulable'
    | 'rate_limited'
    | 'temp_unschedulable'
    | 'overloaded'
    | 'expired'
    | 'quota_exceeded'
  /** 上面那条的可读说明，含恢复时间。 */
  upstream_block_text?: string

  /** 已在 sub2api 生效的状态。 */
  health: ChannelHealth
  /** 引擎期望的状态；与 health 不同说明写回还没落地。 */
  desired_health?: ChannelHealth
  /** 为真时期望状态尚未在 sub2api 生效（含预览模式），渠道实际行为未改变。 */
  apply_pending: boolean
  apply_error?: string
  health_score: number
  short_score: number
  long_score: number
  sample_count: number
  fail_streak: number
  ok_streak: number

  ttfb_p50_ms: number
  ttfb_p95_ms: number

  /** Guardian 内部调度倍率：越低越优先，与网站计费无关。 */
  multiplier: number
  multiplier_manual: boolean
  balance?: number

  rate_multiplier: number
  priority: number
  load_factor?: number
  concurrency: number

  weight: number
  desired_priority: number
  desired_load_factor?: number
  desired_concurrency?: number

  fused_reason: string
  fused_until: string
  cooldown_till: string
  last_sample_at: string
  last_probe_at: string
  last_error: string
  test_model: string
  models: string[]

  /** 最近一次探测请求的模型与 sub2api 实际使用的模型。 */
  last_request_model?: string
  last_probe_model?: string
  /** 为真说明指定的模型被 sub2api 的账号级映射改掉了。 */
  model_rewritten: boolean

  recent: Sample[]
}

export interface GroupState {
  group_id: number
  status: GroupStatus
  strategy: string
  total_accounts: number
  healthy_accounts: number
  degraded_accounts: number
  fused_accounts: number
  paused_accounts: number
  /**
   * 接不到流量的渠道数：人工排除 + sub2api 侧停用 + 被关掉调用 +
   * 临时不可调度 + 过载退避。都计入断供判定，都不算健康。
   *
   * 限流不在这里 —— 它会到点自愈，见 rate_limited_accounts。
   */
  excluded_accounts: number
  /**
   * 正在限流的渠道数（degraded 的子集）：会自愈，与真故障区分开。
   *
   * 判据是上游的 rate_limit_reset_at，不是 Guardian 的探测结果 ——
   * 网站在真实流量里撞到 429 就立刻停止路由，探测要几分钟后才知道。
   */
  rate_limited_accounts: number
  pending_accounts: number
  available_accounts: number
  survivor_account_id?: number
  best_score: number
  avg_score: number
  total_weight: number
  total_concurrency: number
  last_alert_at: string
  last_alert_message: string
  updated_at: string
}

export interface GroupOverride {
  enabled?: boolean
  strategy?: Strategy
  min_pool_size?: number
  weight_budget?: number
  balanced_price_ratio?: number
  breaker_enabled?: boolean
  recovery_enabled?: boolean
  weights_enabled?: boolean
  scaling_enabled?: boolean
  probe_enabled?: boolean
  probe_interval_seconds?: number
  probe_model?: string
}

export interface Group {
  id: number
  name: string
  platform: string
  status: string
  rate_multiplier: number
  managed: boolean
  excluded: boolean
  strategy: Strategy
  state: GroupState
  override?: GroupOverride
  channels: Channel[]
}

export interface EngineStatus {
  running: boolean
  auto_enabled: boolean
  configured: boolean
  monitoring_enabled: boolean
  last_run_at: string
  last_run_ms: number
  last_run_error: string
  last_summary: {
    channels: number
    probed: number
    samples: number
    fused: number
    recovered: number
    applied: number
    cleaned_up: number
    alerts: number
  }
}

export interface StatTile {
  key: string
  label: string
  value: number
  meta: string
  tone: string
}

export interface GuardianEvent {
  id: number
  level: 'info' | 'warn' | 'error'
  action: string
  account_id?: number
  group_id?: number
  message: string
  detail: string
  created_at: string
}

export interface Overview {
  status: EngineStatus
  tiles: StatTile[]
  groups: Group[]
  events: GuardianEvent[]
  total_channels: number
  healthy_channels: number
  pending_channels: number
  degraded_channels: number
  fused_channels: number
  survivor_channels: number
  /**
   * 探测正常但在 sub2api 侧接不到流量的渠道数：被关掉调用 / 临时不可调度 / 过载退避。
   * 不计入健康。限流另计，见 rate_limited_channels。
   */
  unschedulable_channels: number
  /** 处在上游限流窗口里的渠道数（degraded_channels 的子集）。 */
  rate_limited_channels: number
  allocated_concurrency: number
  concurrency_limit: number
  avg_health_score: number
  groups_at_risk: number
  monitoring_enabled: boolean
}

export interface Connection {
  base_url: string
  timeout_seconds: number
  enabled: boolean
  has_admin_key: boolean
}

export interface EventScores {
  perfect: number
  slow_ttfb: number
  upstream_unknown: number
  gateway_error: number
  /** 限流 / 额度耗尽：可自恢复，因此分值非零（0 分会让渠道永远回不了池）。 */
  quota_exhausted: number
  probe_fail: number
  fatal: number
}

export interface Policy {
  strategy: Strategy
  scoring: {
    event_scores: EventScores
    short_window: number
    long_window: number
    latest_weight: number
    short_ratio: number
    slow_ttfb_ms: number
  }
  breaker: {
    enabled: boolean
    hard_fatal: boolean
    http_window: number
    http_failures: number
    http_score_below: number
    latency_window: number
    latency_occurrences: number
    latency_ttfb_ms: number
    max_switch_per_round: number
    min_pool_size: number
    min_pool_score: number
    fused_cooldown_seconds: number
    /** 见到即熔断的状态码，留空则走常规错误率与延迟判定。 */
    instant_status_codes: number[]
    /** 为真时错误率超标只降级不熔断（保留渠道数量）。 */
    http_degrade_only: boolean
    /** 为真时延迟超标只降级不熔断。 */
    latency_degrade_only: boolean
  }
  degrade: {
    enabled: boolean
    score_threshold: number
    priority_step: number
    load_factor_ratio: number
    min_load_factor: number
  }
  recovery: {
    enabled: boolean
    probe_interval_seconds: number
    target_score: number
    success_count: number
    hold_seconds: number
  }
  weights: {
    enabled: boolean
    budget: number
    gate_floor: number
    price_exp: number
    speed_exp: number
    balanced_price_ratio: number
    change_threshold: number
    cooldown_seconds: number
    min_load_factor: number
    max_load_factor: number
  }
  cleanup: {
    enabled: boolean
    action: 'none' | 'pause' | 'disable' | 'delete'
    occurrences: number
    window: number
    min_fused_minutes: number
    max_per_round: number
    keep_last_in_group: boolean
    only_auth_errors: boolean
    trigger_status_codes: number[]
  }
  scaling: {
    enabled: boolean
    global_max_concurrency: number
    min_per_account: number
    max_per_account: number
    scale_up_ratio: number
    step_up: number
    step_down: number
    cooldown_seconds: number
  }
  auto_apply: {
    schedulable: boolean
    priority: boolean
    load_factor: boolean
    concurrency: boolean
  }
  probe: {
    enabled: boolean
    interval_seconds: number
    timeout_seconds: number
    concurrency: number
    model: string
    prompt: string
    skip_when_traffic_fresh: boolean
    traffic_fresh_seconds: number
  }
  traffic: {
    enabled: boolean
    refresh_seconds: number
    lookback_minutes: number
    max_samples_per_account: number
  }
  classify: {
    fatal_patterns: string[]
    gateway_status_codes: number[]
  }
  managed_group_mode: 'all' | 'selected'
  managed_group_ids: number[]
  managed_account_types: string[]
  managed_platforms: string[]
  excluded_group_ids: number[]
  excluded_account_ids: number[]
  paused_account_ids: number[]
  account_test_models: Record<string, string>
  /** 人工设置的调度倍率，键为渠道 ID。 */
  account_multipliers: Record<string, number>
}

export interface Action {
  id: number
  account_id: number
  kind: string
  before: string
  after: string
  ok: boolean
  error: string
  created_at: string

  account_name: string
  platform: string
  groups?: GroupRef[]
  deleted: boolean
}
