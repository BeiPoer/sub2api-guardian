import type { ChannelHealth, GroupStatus } from './types'

/** 健康态 → 中文标签与配色。 */
export function healthMeta(health: ChannelHealth): { label: string; tone: string } {
  switch (health) {
    case 'healthy':
      return { label: '健康', tone: 'success' }
    case 'degraded':
      return { label: '降级', tone: 'warning' }
    case 'fused':
      return { label: '已熔断', tone: 'danger' }
    case 'survivor':
      return { label: '保底强留', tone: 'purple' }
    case 'paused':
      return { label: '已暂停', tone: 'gray' }
    case 'excluded':
      return { label: '已排除', tone: 'gray' }
    default:
      return { label: '待探测', tone: 'gray' }
  }
}

/** 分组状态 → 中文标签与配色。 */
export function groupStatusMeta(status: GroupStatus): { label: string; tone: string } {
  switch (status) {
    case 'healthy':
      return { label: '健康', tone: 'success' }
    case 'partial_degraded':
      return { label: '部分异常', tone: 'warning' }
    case 'rate_limited':
      // 与「部分异常」区分开：限流会自愈、渠道仍在池子里，但也不是健康。
      return { label: '限流中', tone: 'primary' }
    case 'survivor_only':
      return { label: '仅剩保底', tone: 'danger' }
    case 'all_fused':
      return { label: '全部熔断', tone: 'danger' }
    case 'skipped':
      return { label: '未参与', tone: 'gray' }
    default:
      return { label: '无渠道', tone: 'gray' }
  }
}

export function strategyLabel(strategy: string): string {
  switch (strategy) {
    case 'price':
      return '价格优先'
    case 'speed':
      return '速度优先'
    case 'balanced':
      return '均衡'
    default:
      return strategy
  }
}

export function eventLabel(event: string): string {
  switch (event) {
    case 'perfect':
      return '完美健康'
    case 'slow_ttfb':
      return '首字慢'
    case 'upstream_unknown':
      return '上游未知异常'
    case 'gateway_error':
      return '网关/限流错误'
    case 'probe_fail':
      return '探测失败'
    case 'fatal':
      return '致命错误'
    default:
      return event
  }
}

export function levelMeta(level: string): { label: string; tone: string } {
  switch (level) {
    case 'error':
      return { label: '错误', tone: 'danger' }
    case 'warn':
      return { label: '警告', tone: 'warning' }
    default:
      return { label: '信息', tone: 'primary' }
  }
}

export function formatTime(value?: string): string {
  if (!value || value.startsWith('0001-01-01')) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleString('zh-CN', { hour12: false })
}

export function formatRelative(value?: string): string {
  if (!value || value.startsWith('0001-01-01')) return '从未'
  const diff = Date.now() - new Date(value).getTime()
  if (Number.isNaN(diff)) return '从未'
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  return `${Math.floor(diff / 86_400_000)} 天前`
}

export function formatMs(value?: number): string {
  if (!value || value <= 0) return '—'
  if (value < 1000) return `${value}ms`
  return `${(value / 1000).toFixed(2)}s`
}

export function formatPercent(value?: number, digits = 1): string {
  if (value === undefined || value === null) return '—'
  return `${(value * 100).toFixed(digits)}%`
}

/** 调度倍率展示：小数位按量级自适应，避免 0.01 被显示成 0.0。 */
export function formatMultiplier(value?: number): string {
  if (value === undefined || value === null || value <= 0) return '—'
  if (value < 0.1) return value.toFixed(3)
  if (value < 1) return value.toFixed(2)
  return value.toFixed(2)
}

/** 倍率配色：越低越优先，用颜色区分档位。 */
export function multiplierTone(value?: number): string {
  if (value === undefined || value === null || value <= 0) return 'gray'
  if (value <= 0.1) return 'success'
  if (value <= 1) return 'primary'
  if (value <= 3) return 'warning'
  return 'danger'
}
