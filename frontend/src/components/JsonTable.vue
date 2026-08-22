<template>
  <div v-if="rows.length" class="table-container json-table-container" :class="tableClass">
    <table class="table json-table">
      <thead>
        <tr>
          <th v-for="column in columns" :key="column" :class="columnClass(column)">
            {{ columnLabel(column) }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, index) in rows" :key="index">
          <td
            v-for="column in columns"
            :key="column"
            :class="[columnClass(column), { 'json-value': isComplex(row[column]) }]"
            :title="displayValue(row[column], column)"
          >
            {{ displayValue(row[column], column) }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <div v-else class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ emptyText }}</div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

type Row = Record<string, unknown>

const props = withDefaults(
  defineProps<{
    data: unknown
    emptyText?: string
    tableClass?: string
    columnOrder?: string[]
  }>(),
  { emptyText: '暂无数据', tableClass: '', columnOrder: () => [] }
)

const rows = computed<Row[]>(() => {
  if (Array.isArray(props.data)) {
    return props.data.map(item => (isRow(item) ? item : { value: item }))
  }
  if (isRow(props.data)) {
    return Object.entries(props.data).map(([name, value]) => (
      isRow(value) ? { name, ...value } : { name, value }
    ))
  }
  return []
})

const columns = computed(() => {
  const available = Array.from(new Set(rows.value.flatMap(row => Object.keys(row))))
  const preferred = props.columnOrder.filter(column => available.includes(column))
  return [...preferred, ...available.filter(column => !preferred.includes(column))].slice(0, 8)
})

const columnLabels: Record<string, string> = {
  id: 'ID',
  ID: 'ID',
  name: '名称',
  display_name: '显示名称',
  key: '密钥',
  value: '值',
  email: '邮箱',
  username: '账号',
  user_id: '用户 ID',
  role: '角色',
  balance: '余额',
  used_balance: '已用余额',
  total_recharged: '累计充值',
  quota: '额度',
  used_quota: '已用额度',
  quota_used: '已用额度',
  remain_quota: '剩余额度',
  status: '状态',
  active: '启用',
  summary: '摘要',
  group: '分组',
  group_id: '分组 ID',
  groupId: '分组 ID',
  groups: '分组',
  group_ids: '分组 ID',
  allowed_groups: '允许分组',
  schedulable: '可调度',
  error_message: '错误信息',
  before_status: '原状态',
  after_status: '新状态',
  target_type: '目标类型',
  target_id: '目标 ID',
  account_id: '账号 ID',
  account_name: '账号名称',
  site_name: '站点名称',
  ratio: '倍率',
  rate: '倍率',
  rate_multiplier: '倍率',
  user_rate_multiplier: '专属倍率',
  rpm_limit: 'RPM 限制',
  concurrency: '并发数',
  run_mode: '运行模式',
  user_group: '用户分组',
  platform: '平台',
  description: '描述',
  type: '类型',
  plan: '套餐',
  subscription_type: '订阅类型',
  active_count: '活跃数',
  total: '总数',
  count: '数量',
  items: '项目',
  created_at: '创建时间',
  updated_at: '更新时间',
  expires_at: '过期时间',
  expired_time: '过期时间',
  last_active_at: '最近活跃',
  last_used_at: '最近使用',
  accessed_time: '最近访问',
  created_time: '创建时间',
  ip_whitelist: 'IP 白名单',
  ip_blacklist: 'IP 黑名单',
  identities: '身份',
  identity_bindings: '身份绑定',
  auth_bindings: '认证绑定',
  email_bound: '邮箱绑定',
  dingtalk_bound: '钉钉绑定',
  wechat_bound: '微信绑定',
  linuxdo_bound: 'LinuxDO 绑定',
  oidc_bound: 'OIDC 绑定',
  balance_notify_enabled: '余额通知',
  balance_notify_threshold: '余额通知阈值',
  balance_notify_threshold_type: '余额通知阈值类型',
  balance_notify_extra_emails: '余额通知额外邮箱',
  allow_image_generation: '允许生图',
  allow_messages_dispatch: '允许消息调度',
  claude_code_only: '仅 Claude Code',
  daily_limit_usd: '每日限额',
  weekly_limit_usd: '每周限额',
  monthly_limit_usd: '每月限额',
  fallback_group_id: '备用分组 ID',
  fallback_group_id_on_invalid_request: '无效请求备用分组 ID',
  image_price_1k: '图片 1K 价格',
  image_price_2k: '图片 2K 价格',
  image_price_4k: '图片 4K 价格',
  image_rate_independent: '图片独立倍率',
  image_rate_multiplier: '图片倍率',
  is_exclusive: '专属分组',
  require_oauth_only: '仅 OAuth',
  require_privacy_set: '要求隐私设置',
  unlimited_quota: '不限额度',
  model_limits_enabled: '模型限制',
  model_limits: '模型列表',
  allow_ips: '允许 IP',
  cross_group_retry: '跨组重试',
  rate_limit_5h: '5 小时限速',
  rate_limit_1d: '1 日限速',
  rate_limit_7d: '7 日限速',
  usage_5h: '5 小时用量',
  usage_1d: '1 日用量',
  usage_7d: '7 日用量',
  window_5h_start: '5 小时窗口开始',
  window_1d_start: '1 日窗口开始',
  window_7d_start: '7 日窗口开始',
  raw: '原始数据',
  error: '错误',
  message: '消息'
}

const labelTokenMap: Record<string, string> = {
  active: '活跃',
  allow: '允许',
  allowed: '允许',
  balance: '余额',
  blacklist: '黑名单',
  bindings: '绑定',
  bound: '绑定',
  claude: 'Claude',
  code: 'Code',
  concurrency: '并发',
  count: '数量',
  created: '创建',
  cross: '跨',
  daily: '每日',
  description: '描述',
  dispatch: '调度',
  email: '邮箱',
  enabled: '启用',
  error: '错误',
  exclusive: '专属',
  expired: '过期',
  expires: '过期',
  extra: '额外',
  fallback: '备用',
  generation: '生成',
  group: '分组',
  groups: '分组',
  id: 'ID',
  identities: '身份',
  identity: '身份',
  image: '图片',
  independent: '独立',
  invalid: '无效',
  ip: 'IP',
  key: '密钥',
  last: '最近',
  limit: '限制',
  linuxdo: 'LinuxDO',
  messages: '消息',
  mode: '模式',
  monthly: '每月',
  name: '名称',
  notify: '通知',
  oauth: 'OAuth',
  oidc: 'OIDC',
  only: '仅',
  platform: '平台',
  price: '价格',
  privacy: '隐私',
  quota: '额度',
  rate: '倍率',
  recharged: '充值',
  remain: '剩余',
  request: '请求',
  require: '要求',
  retry: '重试',
  role: '角色',
  rpm: 'RPM',
  run: '运行',
  start: '开始',
  status: '状态',
  subscription: '订阅',
  threshold: '阈值',
  time: '时间',
  total: '累计',
  type: '类型',
  updated: '更新',
  usage: '用量',
  used: '已用',
  user: '用户',
  weekly: '每周',
  whitelist: '白名单',
  window: '窗口'
}

const valueLabels: Record<string, string> = {
  active: '启用',
  inactive: '停用',
  disabled: '禁用',
  enabled: '启用',
  expired: '已过期',
  quota_exhausted: '额度耗尽',
  exhausted: '已耗尽',
  pending: '待处理',
  error: '异常',
  success: '成功',
  failed: '失败',
  standard: '标准',
  premium: '高级',
  trial: '试用',
  free: '免费',
  user: '用户',
  admin: '管理员',
  owner: '所有者',
  root: '超级管理员',
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  auto: '自动'
}

const translatedColumns = new Set(['status', 'role', 'subscription_type', 'platform', 'type', 'run_mode'])

function isRow(value: unknown): value is Row {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function isComplex(value: unknown): boolean {
  return value !== null && typeof value === 'object'
}

function valuePreview(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value) || '—'
  } catch {
    return String(value)
  }
}

function displayValue(value: unknown, column = ''): string {
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'string' && translatedColumns.has(column)) return valueLabels[value.toLowerCase()] || value
  return valuePreview(value)
}

function columnLabel(column: string): string {
  if (columnLabels[column]) return columnLabels[column]
  return column
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map(part => labelTokenMap[part.toLowerCase()] || part.toUpperCase())
    .join(' ')
}

function columnClass(column: string): string {
  const normalized = column.replace(/[^a-z0-9_-]+/gi, '_').replace(/^_+|_+$/g, '').toLowerCase()
  return normalized ? `json-column json-column-${normalized}` : 'json-column'
}
</script>

<style scoped>
.json-table {
  min-width: 740px;
  table-layout: fixed;
}

.json-table th,
.json-table td {
  vertical-align: middle;
  white-space: normal;
}

.json-table td.json-value {
  overflow: visible;
  text-overflow: clip;
  overflow-wrap: anywhere;
  word-break: break-word;
  line-height: 1.35;
}

.json-table-container.group-cache-table .json-column-id {
  width: 52px;
  min-width: 52px;
  max-width: 52px;
}

.json-table-container.group-cache-table .json-column-name {
  width: 230px;
  min-width: 230px;
}

.json-table-container.group-cache-table .json-column-ratio,
.json-table-container.group-cache-table .json-column-rate,
.json-table-container.group-cache-table .json-column-rate_multiplier,
.json-table-container.group-cache-table .json-column-group_ratio,
.json-table-container.group-cache-table .json-column-model_ratio {
  width: 64px;
  min-width: 64px;
  max-width: 64px;
}
</style>
