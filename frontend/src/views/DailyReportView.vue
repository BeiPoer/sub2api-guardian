<template>
  <AppLayout title="每日报告" subtitle="按配置的执行小时汇总报告源站当天的使用、注册和充值数据，并发送企微通知">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载每日报告…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">每日报告</h2>
            <Badge :tone="form.enabled ? 'success' : 'gray'" dot>
              {{ form.enabled ? '已启用' : '已关闭' }}
            </Badge>
          </div>
          <p class="mt-1 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
            每天汇总当天消耗额度、Token、注册人数和充值量；统计完成后使用通知配置中的企微应用发送普通文本消息。
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="runNow">
            <Icon name="play" size="sm" />
            {{ running ? '执行中…' : '立即执行' }}
          </button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
            <Icon name="check" size="sm" />
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>

      <div
        v-if="error"
        class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
      >
        <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
        <span>{{ error }}</span>
      </div>

      <div class="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-gray-500 dark:text-dark-400">
        <span class="flex items-center gap-2">
          最近运行状态
          <Badge :tone="runStatusMeta(config.last_status).tone" dot>{{ runStatusMeta(config.last_status).label }}</Badge>
        </span>
        <span>执行时间 {{ formatHour(form.run_hour) }}</span>
        <span>时区 {{ form.timezone }}</span>
        <span v-if="config.last_error" class="flex min-w-0 items-center gap-1 text-red-600 dark:text-red-300" :title="config.last_error">
          <Icon name="exclamationTriangle" size="xs" />
          <span class="max-w-xl truncate">{{ config.last_error }}</span>
        </span>
      </div>

      <section class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary"><Icon name="dollar" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestSummary ? formatQuotaAmount(latestSummary.total_actual_cost, latestSummary.quota_unit) : '—' }}</p>
            <p class="stat-label">今日消耗额度</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-purple"><Icon name="chartBar" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestSummary ? formatTokens(latestSummary.total_tokens) : '—' }}</p>
            <p class="stat-label">今日总 Token</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-success"><Icon name="users" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestSummary ? formatNumber(latestSummary.new_users) : '—' }}</p>
            <p class="stat-label">今日注册人数</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-warning"><Icon name="creditCard" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestSummary ? rechargeText(latestSummary) : '—' }}</p>
            <p class="stat-label">今日充值量</p>
            <p v-if="latestSummary" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              充值人数 {{ formatNumber(latestSummary.recharge_users) }} 人
            </p>
          </div>
        </div>
      </section>

      <form class="space-y-6" @submit.prevent="save">
        <section class="card">
          <div class="card-header flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">调度设置</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                定时检查每分钟判断一次；每日按一次配置的小时执行，立即执行会忽略时间点但仍遵守报告互斥锁。
              </p>
            </div>
            <RouterLink to="/reports/notifications" class="btn btn-secondary btn-sm">
              <Icon name="bell" size="sm" />
              通知配置
            </RouterLink>
          </div>
          <div class="space-y-5 p-6">
            <SwitchRow
              v-model="form.enabled"
              label="启用每日报告"
              description="启用后每天在配置的小时执行，并使用共享企微配置发送统计结果。"
            />
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field
                v-model="form.run_hour"
                label="每日执行小时"
                suffix="时"
                type="number"
                :min="0"
                :max="23"
                hint="默认 23 时；取值范围为 0–23。"
              />
              <Field
                v-model="form.timezone"
                label="时区"
                placeholder="Asia/Shanghai"
                hint="统计当天和执行时间均按此时区计算。"
              />
            </div>
          </div>
        </section>
      </form>

      <section class="card">
        <div class="card-header flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">报告源站</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">每日消耗、Token、注册和充值统计都由当前共享源站提供。</p>
          </div>
          <RouterLink to="/reports/source" class="btn btn-secondary btn-sm">
            <Icon name="globe" size="sm" />
            源站配置
          </RouterLink>
        </div>
        <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-3">
          <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">模式 / 类型</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ source.mode === 'global' ? '全局' : '自定义' }} / {{ sourceTypeLabel(source.type) }}
            </p>
          </div>
          <div class="min-w-0 rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">连接地址</p>
            <p class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">{{ source.base_url || '未配置' }}</p>
          </div>
          <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">配置状态</p>
            <div class="mt-1 flex flex-wrap items-center gap-2">
              <Badge :tone="source.configured ? 'success' : 'danger'" dot>
                {{ source.configured ? '已配置，可读取报告数据' : '未完成，请先配置源站' }}
              </Badge>
              <RouterLink v-if="!source.configured" to="/reports/source" class="text-xs text-primary-600 hover:underline dark:text-primary-300">
                前往配置
              </RouterLink>
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">最近统计</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              {{ latestSummary ? `统计日期：${latestSummary.date}（${latestSummary.timezone}）` : '尚未执行过每日报告。' }}
            </p>
          </div>
          <Badge v-if="latestRun" :tone="runStatusMeta(latestRun.status).tone" dot>{{ runStatusMeta(latestRun.status).label }}</Badge>
        </div>
        <div v-if="latestSummary" class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 xl:grid-cols-4">
          <div class="rounded-xl border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">消耗额度</p>
            <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatQuotaAmount(latestSummary.total_actual_cost, latestSummary.quota_unit) }}</p>
          </div>
          <div class="rounded-xl border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">总 Token</p>
            <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatTokens(latestSummary.total_tokens) }}</p>
          </div>
          <div class="rounded-xl border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">注册人数</p>
            <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatNumber(latestSummary.new_users) }} 人</p>
          </div>
          <div class="rounded-xl border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">充值量</p>
            <div v-if="rechargeEntries(latestSummary).length" class="mt-1 space-y-1 text-sm font-semibold text-gray-900 dark:text-white">
              <p v-for="[currency, amount] in rechargeEntries(latestSummary)" :key="currency">
                {{ currency }} {{ formatAmount(amount) }}
              </p>
            </div>
            <p v-else class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">0</p>
            <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">充值人数 {{ formatNumber(latestSummary.recharge_users) }} 人</p>
          </div>
        </div>
        <div v-else class="p-6 text-sm text-gray-500 dark:text-dark-400">
          保存配置后，可以先用“立即执行”生成一份当天统计。
        </div>
      </section>

      <section class="card">
        <div class="card-header flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">最近 7 天运行记录</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">保存每次执行的统计日期、四项汇总和企微投递结果。</p>
          </div>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="refreshing" @click="refreshHistory">
            <Icon name="refresh" size="sm" />
            {{ refreshing ? '刷新中…' : '刷新' }}
          </button>
        </div>
        <div v-if="!runs.length" class="p-6">
          <EmptyState icon="chartBar" title="暂无运行记录" description="保存配置后，可以先用“立即执行”验证统计和通知链路。" />
        </div>
        <div v-else class="table-wrapper">
          <table class="table min-w-[760px]">
            <thead>
              <tr>
                <th>执行时间</th>
                <th>状态</th>
                <th>统计日期</th>
                <th>消耗 / Token</th>
                <th>注册人数</th>
                <th>充值人数 / 充值量</th>
                <th>企微投递</th>
                <th class="w-24">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="run in runs" :key="run.id">
                <td class="whitespace-nowrap text-xs">{{ formatTime(run.started_at) }}</td>
                <td>
                  <Badge :tone="runStatusMeta(run.status).tone" dot>{{ runStatusMeta(run.status).label }}</Badge>
                  <p v-if="run.message" class="mt-1 max-w-40 truncate text-xs text-gray-500 dark:text-dark-400" :title="run.message">{{ run.message }}</p>
                </td>
                <td class="whitespace-nowrap text-xs text-gray-500 dark:text-dark-400">{{ run.summary?.date || '—' }}</td>
                <td class="whitespace-nowrap text-xs tabular-nums">
                  <span>{{ run.summary ? formatQuotaAmount(run.summary.total_actual_cost, run.summary.quota_unit) : '—' }}</span>
                  <span class="mx-1 text-gray-300 dark:text-dark-600">/</span>
                  <span>{{ run.summary ? formatTokens(run.summary.total_tokens) : '—' }}</span>
                </td>
                <td class="whitespace-nowrap text-xs tabular-nums">
                  <span>{{ run.summary ? `${formatNumber(run.summary.new_users)} 人` : '—' }}</span>
                </td>
                <td class="whitespace-nowrap text-xs tabular-nums">
                  <span>{{ run.summary ? `${formatNumber(run.summary.recharge_users)} 人` : '—' }}</span>
                  <span class="mx-1 text-gray-300 dark:text-dark-600">/</span>
                  <span>{{ run.summary ? rechargeText(run.summary) : '—' }}</span>
                </td>
                <td>
                  <Badge :tone="notificationMeta(run.notification_status).tone">{{ notificationMeta(run.notification_status).label }}</Badge>
                  <p v-if="run.notification_error" class="mt-1 max-w-44 truncate text-xs text-red-500" :title="run.notification_error">{{ run.notification_error }}</p>
                </td>
                <td>
                  <button type="button" class="btn btn-ghost btn-sm" @click="openRun(run)">
                    <Icon name="eye" size="sm" />
                    查看
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <MiniPager
          v-if="total > pageSize"
          :page="page"
          :page-size="pageSize"
          :total="total"
          @update:page="loadRuns"
        />
      </section>
    </template>

    <Modal
      :open="Boolean(selectedRun)"
      title="每日报告运行结果"
      :subtitle="selectedRun ? `${formatTime(selectedRun.started_at)} · ${runStatusMeta(selectedRun.status).label}` : ''"
      @close="selectedRun = null"
    >
      <template v-if="selectedRun">
        <div v-if="selectedRun.summary" class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">消耗额度</p><p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatQuotaAmount(selectedRun.summary.total_actual_cost, selectedRun.summary.quota_unit) }}</p></div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">总 Token</p><p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatTokens(selectedRun.summary.total_tokens) }}</p></div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">注册人数</p><p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatNumber(selectedRun.summary.new_users) }}</p></div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">统计日期</p><p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ selectedRun.summary.date }}</p></div>
        </div>
        <div v-if="selectedRun.summary" class="mt-4 rounded-lg border border-gray-100 p-4 dark:border-dark-700">
          <p class="text-xs text-gray-500 dark:text-dark-400">充值量</p>
          <div v-if="rechargeEntries(selectedRun.summary).length" class="mt-2 space-y-1 text-sm text-gray-900 dark:text-white">
            <p v-for="[currency, amount] in rechargeEntries(selectedRun.summary)" :key="currency">
              {{ currency }}：{{ formatAmount(amount) }}
            </p>
          </div>
          <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">0</p>
          <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">充值人数：{{ formatNumber(selectedRun.summary.recharge_users) }} 人</p>
        </div>
        <div v-if="selectedRun.error" class="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ selectedRun.error }}</div>
        <div v-if="selectedRun.notification_error" class="mt-4 rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-300">企微：{{ selectedRun.notification_error }}</div>
        <div class="mt-4 grid grid-cols-1 gap-3 text-sm text-gray-500 dark:text-dark-400 sm:grid-cols-2">
          <p>统计窗口：{{ formatTime(selectedRun.window_start) }} 至 {{ formatTime(selectedRun.window_end) }}</p>
          <p>完成时间：{{ formatTime(selectedRun.finished_at) }}</p>
        </div>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import EmptyState from '@/components/EmptyState.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import MiniPager from '@/components/MiniPager.vue'
import Modal from '@/components/Modal.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import type {
  DailyReportRun,
  DailyReportSaveInput,
  DailyReportSummary,
  DailyReportView
} from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const loading = ref(true)
const saving = ref(false)
const running = ref(false)
const refreshing = ref(false)
const error = ref('')
const view = ref<DailyReportView | null>(null)
const runs = ref<DailyReportRun[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const selectedRun = ref<DailyReportRun | null>(null)

const form = ref<DailyReportSaveInput>({
  enabled: false,
  run_hour: 23,
  timezone: 'Asia/Shanghai'
})

const config = computed(() => view.value?.config ?? {
  enabled: form.value.enabled,
  run_hour: form.value.run_hour,
  timezone: form.value.timezone,
  last_run_at: '',
  last_status: 'never',
  last_error: '',
  next_run_at: ''
})
const latestRun = computed(() => view.value?.latest_run ?? runs.value[0] ?? null)
const latestSummary = computed(() => latestRun.value?.summary ?? null)
const source = computed(() => view.value?.source ?? {
  mode: 'global' as const,
  type: 'sub2api' as const,
  configured: false,
  base_url: ''
})
const busy = computed(() => saving.value || running.value || refreshing.value)

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    const report = await api.dailyReport()
    view.value = report
    applyView(report)
    await loadRuns(1)
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function applyView(report: DailyReportView) {
  form.value = {
    enabled: report.config.enabled,
    run_hour: report.config.run_hour,
    timezone: report.config.timezone
  }
}

async function loadRuns(nextPage: number) {
  const result = await api.dailyReportRuns(nextPage, pageSize)
  runs.value = result.items
  total.value = result.total
  page.value = result.page
}

async function refreshHistory() {
  refreshing.value = true
  error.value = ''
  try {
    const [report] = await Promise.all([api.dailyReport(), loadRuns(page.value)])
    view.value = report
    applyView(report)
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    refreshing.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const report = await api.saveDailyReport({
      enabled: form.value.enabled,
      run_hour: Number(form.value.run_hour),
      timezone: form.value.timezone
    })
    view.value = report
    applyView(report)
    ui.notify('success', '每日报告配置已保存')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    saving.value = false
  }
}

async function runNow() {
  running.value = true
  error.value = ''
  try {
    const result = await api.runDailyReport()
    const run = result.run
    ui.notify(run.status === 'error' ? 'error' : run.notification_status === 'failed' ? 'warning' : 'success', run.message || '每日报告执行完成')
    await refreshHistory()
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    running.value = false
  }
}

function openRun(run: DailyReportRun) {
  selectedRun.value = run
}

function runStatusMeta(status: string) {
  if (status === 'error') return { label: '失败', tone: 'danger' }
  if (status === 'never') return { label: '尚未运行', tone: 'gray' }
  return { label: '成功', tone: 'success' }
}

function notificationMeta(status: string) {
  if (status === 'sent') return { label: '已发送', tone: 'success' }
  if (status === 'failed') return { label: '发送失败', tone: 'danger' }
  if (status === 'disabled') return { label: '未启用', tone: 'gray' }
  return { label: '无需发送', tone: 'gray' }
}

function formatHour(hour: number) {
  return `${String(hour).padStart(2, '0')}:00`
}

function formatNumber(value: number) {
  return Number(value || 0).toLocaleString('zh-CN')
}

function formatTokens(value: number) {
  const tokens = Number(value || 0)
  if (!Number.isFinite(tokens)) return '0'
  if (tokens >= 1_000_000_000) return `${(tokens / 1_000_000_000).toFixed(2)}B`
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(2)}M`
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(2)}K`
  return tokens.toLocaleString('zh-CN')
}

function formatAmount(value: number) {
  return Number(value || 0).toFixed(2)
}

function formatQuotaAmount(value: number, unit?: string) {
  return `${formatAmount(value)}${unit ? ` ${unit}` : ''}`
}

function rechargeEntries(summary: DailyReportSummary) {
  return Object.entries(summary.recharge_amounts || {}).sort(([a], [b]) => a.localeCompare(b))
}

function rechargeText(summary: DailyReportSummary) {
  const entries = rechargeEntries(summary)
  if (!entries.length) return '0'
  if (entries.length === 1) return `${entries[0][0]} ${formatAmount(entries[0][1])}`
  return `${entries.length} 个币种`
}

function sourceTypeLabel(type: string) {
  return type === 'newapi' ? 'New API' : 'Sub2API'
}
</script>
