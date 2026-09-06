<template>
  <AppLayout title="周期报告" subtitle="使用、注册与充值统计">
    <div v-if="loading" class="flex min-h-64 items-center justify-center text-sm text-gray-500">正在加载周期报告…</div>
    <template v-else>
      <div class="flex flex-wrap items-center justify-between gap-4">
        <h2 class="text-xl font-semibold text-gray-900 dark:text-white">周期报告</h2>
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
          <Icon name="check" size="sm" />{{ saving ? '保存中…' : '保存配置' }}
        </button>
      </div>
      <div v-if="error" role="alert" class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>

      <section class="border-b border-gray-200 pb-6 dark:border-dark-700">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">共享源站</h3>
          <RouterLink to="/reports/source" class="btn btn-secondary btn-sm"><Icon name="globe" size="sm" />源站配置</RouterLink>
        </div>
        <div class="flex flex-wrap items-end gap-4">
          <label class="block w-full max-w-xl">
            <span class="input-label">日报与周报源站</span>
            <select v-model="draft.source_id" class="input" :disabled="saving">
              <option v-for="item in view?.sources" :key="item.id" :value="item.id">{{ item.name }} · {{ item.type === 'newapi' ? 'New API' : 'Sub2API' }}</option>
            </select>
          </label>
          <Badge :tone="source?.configured ? 'success' : 'danger'" dot>{{ source?.configured ? '已配置' : '未配置' }}</Badge>
        </div>
        <p class="mt-2 break-all text-xs text-gray-500 dark:text-dark-400">{{ source?.base_url || '无连接地址' }}</p>
      </section>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="tabs" role="tablist" aria-label="报告周期">
          <button v-for="period in periods" :id="`tab-${period.value}`" :key="period.value" type="button" role="tab"
            :aria-label="period.label" :aria-selected="active === period.value" aria-controls="periodic-panel" class="tab flex items-center gap-2"
            :class="active === period.value && 'tab-active'" @click="active = period.value">
            {{ period.label }}
            <span class="h-1.5 w-1.5 rounded-full" :class="draft[period.value].enabled ? 'bg-emerald-500' : 'bg-gray-400'" :title="draft[period.value].enabled ? '已启用' : '已关闭'" />
          </button>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="runNow">
          <Icon name="play" size="sm" />{{ running ? '执行中…' : `立即执行${periodName}` }}
        </button>
      </div>

      <div id="periodic-panel" role="tabpanel" :aria-labelledby="`tab-${active}`" class="space-y-6">
        <form class="space-y-5 border-b border-gray-200 pb-6 dark:border-dark-700" @submit.prevent="save">
          <fieldset :disabled="saving" class="space-y-5">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ periodName }}调度</h3>
            <RouterLink to="/reports/notifications" class="btn btn-secondary btn-sm"><Icon name="bell" size="sm" />通知配置</RouterLink>
          </div>
          <div class="flex max-w-xl items-center justify-between gap-4">
            <span class="text-sm font-medium text-gray-900 dark:text-white">启用{{ periodName }}</span>
            <Toggle v-model="form.enabled" :aria-label="`启用${periodName}`" />
          </div>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <label v-if="active === 'weekly'" class="block">
              <span class="input-label">执行星期</span>
              <select v-model="form.weekday" class="input">
                <option v-for="(day, index) in weekdays" :key="day" :value="index + 1">{{ day }}</option>
              </select>
            </label>
            <Field v-model="form.run_hour" label="执行小时" type="number" suffix="时" :min="0" :max="23" />
            <Field v-model="form.timezone" label="时区" placeholder="Asia/Shanghai" />
            <div class="sm:col-span-2">
              <Field v-model="form.wecom_target" label="企微接收人（留空使用全局）" placeholder="@all 或 zhangsan|lisi" />
            </div>
          </div>
          <div class="flex flex-wrap gap-x-6 gap-y-2 text-sm text-gray-500 dark:text-dark-400">
            <span>统计范围：{{ active === 'weekly' ? '本周一 00:00 至执行时刻' : '当天 00:00 至执行时刻' }}</span>
            <span>下次执行：{{ config?.next_run_at ? formatTime(config.next_run_at) : '未启用' }}</span>
            <span v-if="config">最近状态：<Badge :tone="runStatusMeta(config.last_status).tone">{{ runStatusMeta(config.last_status).label }}</Badge></span>
          </div>
          <p v-if="config?.last_error" class="break-words text-sm text-red-600 dark:text-red-300">{{ config.last_error }}</p>
          </fieldset>
        </form>

        <section class="border-b border-gray-200 pb-6 dark:border-dark-700">
          <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">最近{{ periodName }}统计</h3>
              <p v-if="latestSummary" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ latestSummary.date }}（{{ latestSummary.timezone }}）</p>
            </div>
            <Badge v-if="latestRun" :tone="runStatusMeta(latestRun.status).tone">{{ runStatusMeta(latestRun.status).label }}</Badge>
          </div>
          <div v-if="latestSummary" class="grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-4">
            <div v-for="stat in stats" :key="stat.label" class="min-w-0">
              <p class="flex items-center gap-2 text-xs text-gray-500 dark:text-dark-400"><Icon :name="stat.icon" size="sm" />{{ stat.label }}</p>
              <p class="mt-2 break-words text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ stat.value }}</p>
            </div>
          </div>
          <p v-else class="text-sm text-gray-500 dark:text-dark-400">暂无统计结果</p>
          <p v-if="latestRun" class="mt-4 text-xs text-gray-500 dark:text-dark-400">统计窗口：{{ formatTime(latestRun.window_start) }} 至 {{ formatTime(latestRun.window_end) }}</p>
        </section>

        <section>
          <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">最近 {{ active === 'weekly' ? 30 : 7 }} 天运行记录</h3>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refreshHistory"><Icon name="refresh" size="sm" />{{ refreshing ? '刷新中…' : '刷新' }}</button>
          </div>
          <EmptyState v-if="!history.items.length" icon="chartBar" title="暂无运行记录" />
          <div v-else class="table-wrapper">
            <table class="table min-w-[900px]">
              <thead><tr><th>执行时间</th><th>状态</th><th>统计范围</th><th>消耗 / Token</th><th>注册人数</th><th>充值人数 / 充值量</th><th>企微投递</th><th class="w-16">操作</th></tr></thead>
              <tbody>
                <tr v-for="run in history.items" :key="run.id">
                  <td class="whitespace-nowrap text-xs">{{ formatTime(run.started_at) }}</td>
                  <td><Badge :tone="runStatusMeta(run.status).tone">{{ runStatusMeta(run.status).label }}</Badge><p v-if="run.message" class="mt-1 max-w-40 truncate text-xs text-gray-500" :title="run.message">{{ run.message }}</p></td>
                  <td class="text-xs">{{ run.summary?.date || '—' }}</td>
                  <td class="whitespace-nowrap text-xs tabular-nums">{{ run.summary ? `${formatQuotaAmount(run.summary.total_actual_cost, run.summary.quota_unit)} / ${formatTokens(run.summary.total_tokens)}` : '—' }}</td>
                  <td class="text-xs">{{ run.summary ? `${formatNumber(run.summary.new_users)} 人` : '—' }}</td>
                  <td class="text-xs tabular-nums">{{ run.summary ? `${formatNumber(run.summary.recharge_users)} 人 / ${rechargeText(run.summary)}` : '—' }}</td>
                  <td><Badge :tone="notificationMeta(run.notification_status).tone">{{ notificationMeta(run.notification_status).label }}</Badge><p v-if="run.notification_error" class="mt-1 max-w-40 truncate text-xs text-red-500" :title="run.notification_error">{{ run.notification_error }}</p></td>
                  <td><button type="button" class="btn btn-ghost btn-sm" title="查看运行结果" aria-label="查看运行结果" @click="selectedRun = run"><Icon name="eye" size="sm" /></button></td>
                </tr>
              </tbody>
            </table>
          </div>
          <MiniPager v-if="history.total > pageSize" :page="history.page" :page-size="pageSize" :total="history.total" @update:page="changePage" />
        </section>
      </div>
    </template>

    <Modal :open="Boolean(selectedRun)" :title="`${periodName}运行结果`" :subtitle="selectedRun ? formatTime(selectedRun.started_at) : ''" @close="selectedRun = null">
      <template v-if="selectedRun">
        <dl v-if="selectedRun.summary" class="grid grid-cols-1 gap-4 text-sm sm:grid-cols-2">
          <div><dt class="text-gray-500">统计范围</dt><dd class="mt-1 break-words">{{ selectedRun.summary.date }}（{{ selectedRun.summary.timezone }}）</dd></div>
          <div><dt class="text-gray-500">消耗额度</dt><dd class="mt-1">{{ formatQuotaAmount(selectedRun.summary.total_actual_cost, selectedRun.summary.quota_unit) }}</dd></div>
          <div><dt class="text-gray-500">总 Token</dt><dd class="mt-1">{{ formatTokens(selectedRun.summary.total_tokens) }}</dd></div>
          <div><dt class="text-gray-500">注册人数</dt><dd class="mt-1">{{ formatNumber(selectedRun.summary.new_users) }} 人</dd></div>
          <div><dt class="text-gray-500">充值量</dt><dd class="mt-1">{{ rechargeText(selectedRun.summary) }}</dd></div>
          <div><dt class="text-gray-500">充值人数</dt><dd class="mt-1">{{ formatNumber(selectedRun.summary.recharge_users) }} 人</dd></div>
        </dl>
        <p v-if="selectedRun.error" class="mt-4 break-words text-sm text-red-600">{{ selectedRun.error }}</p>
        <p v-if="selectedRun.notification_error" class="mt-4 break-words text-sm text-amber-600">企微：{{ selectedRun.notification_error }}</p>
        <div class="mt-4 space-y-2 text-xs text-gray-500 dark:text-dark-400">
          <p>统计窗口：{{ formatTime(selectedRun.window_start) }} 至 {{ formatTime(selectedRun.window_end) }}</p>
          <p>完成时间：{{ formatTime(selectedRun.finished_at) }}</p>
          <p>企微投递：{{ notificationMeta(selectedRun.notification_status).label }}</p>
        </div>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import EmptyState from '@/components/EmptyState.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import MiniPager from '@/components/MiniPager.vue'
import Modal from '@/components/Modal.vue'
import Toggle from '@/components/Toggle.vue'
import { api } from '@/lib/api'
import { formatTime } from '@/lib/format'
import type { DailyReportRun, DailyReportSummary, PeriodicReportSaveInput, PeriodicReportView, PeriodicScheduleInput, ReportPeriod } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const route = useRoute()
const router = useRouter()
const periods: { value: ReportPeriod; label: string }[] = [{ value: 'daily', label: '每日' }, { value: 'weekly', label: '每周' }]
const weekdays = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']
const active = computed<ReportPeriod>({
  get: () => route.query.period === 'weekly' ? 'weekly' : 'daily',
  set: period => { void router.replace({ query: { ...route.query, period } }) }
})
const periodName = computed(() => active.value === 'weekly' ? '周报' : '日报')
const loading = ref(true)
const saving = ref(false)
const running = ref(false)
const refreshing = ref(false)
const busy = computed(() => !view.value || saving.value || running.value || refreshing.value)
const error = ref('')
const view = ref<PeriodicReportView | null>(null)
const draft = ref<PeriodicReportSaveInput>({
  source_id: 'global',
  daily: { enabled: false, run_hour: 23, timezone: 'Asia/Shanghai', wecom_target: '' },
  weekly: { enabled: false, run_hour: 9, weekday: 1, timezone: 'Asia/Shanghai', wecom_target: '' }
})
const form = computed(() => draft.value[active.value])
const config = computed(() => view.value?.[active.value].config)
const source = computed(() => view.value?.sources.find(item => item.id === draft.value.source_id))
const pageSize = 20
const histories = ref<Record<ReportPeriod, { items: DailyReportRun[]; total: number; page: number }>>({
  daily: { items: [], total: 0, page: 1 }, weekly: { items: [], total: 0, page: 1 }
})
const history = computed(() => histories.value[active.value])
const latestRun = computed(() => view.value?.[active.value].latest_run ?? history.value.items[0] ?? null)
const latestSummary = computed(() => latestRun.value?.summary ?? null)
const selectedRun = ref<DailyReportRun | null>(null)
watch(active, () => { selectedRun.value = null })
const stats = computed(() => {
  const summary = latestSummary.value
  if (!summary) return []
  return [
    { label: '消耗额度', value: formatQuotaAmount(summary.total_actual_cost, summary.quota_unit), icon: 'dollar' as const },
    { label: '总 Token', value: formatTokens(summary.total_tokens), icon: 'chartBar' as const },
    { label: '注册人数', value: `${formatNumber(summary.new_users)} 人`, icon: 'users' as const },
    { label: `充值量（${formatNumber(summary.recharge_users)} 人）`, value: rechargeText(summary), icon: 'creditCard' as const }
  ]
})

function scheduleInput(schedule: PeriodicScheduleInput): PeriodicScheduleInput {
  return { enabled: schedule.enabled, run_hour: Number(schedule.run_hour), timezone: schedule.timezone, wecom_target: schedule.wecom_target, ...(schedule.weekday === undefined ? {} : { weekday: Number(schedule.weekday) }) }
}

function applyView(report: PeriodicReportView) {
  view.value = report
  draft.value = { source_id: report.source_id, daily: scheduleInput(report.daily.config), weekly: scheduleInput(report.weekly.config) }
}

onMounted(async () => {
  try {
    applyView(await api.periodicReport())
    await Promise.all(periods.map(period => loadRuns(period.value, 1)))
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
})

async function loadRuns(period: ReportPeriod, page: number) {
  histories.value[period] = await api.periodicReportRuns(period, page, pageSize)
}

async function changePage(page: number) {
  try { await loadRuns(active.value, page) } catch (err) { error.value = (err as Error).message }
}

async function refreshHistory() {
  refreshing.value = true
  error.value = ''
  const period = active.value
  try {
    const [report] = await Promise.all([api.periodicReport(), loadRuns(period, histories.value[period].page)])
    view.value = report
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
    applyView(await api.savePeriodicReport({ source_id: draft.value.source_id, daily: scheduleInput(draft.value.daily), weekly: scheduleInput(draft.value.weekly) }))
    ui.notify('success', '周期报告配置已保存')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    saving.value = false
  }
}

async function runNow() {
  const period = active.value
  running.value = true
  error.value = ''
  try {
    const { run } = await api.runPeriodicReport(period)
    ui.notify(run.status === 'error' ? 'error' : run.notification_status === 'failed' ? 'warning' : 'success', run.message)
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    try {
      const [report] = await Promise.all([api.periodicReport(), loadRuns(period, 1)])
      view.value = report
    } catch (err) { error.value = (err as Error).message }
    running.value = false
  }
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

function formatNumber(value: number) { return Number(value || 0).toLocaleString('zh-CN') }
function formatTokens(value: number) {
  const tokens = Number(value || 0)
  if (!Number.isFinite(tokens)) return '0'
  if (tokens >= 1_000_000_000) return `${(tokens / 1_000_000_000).toFixed(2)}B`
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(2)}M`
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(2)}K`
  return tokens.toLocaleString('zh-CN')
}
function formatQuotaAmount(value: number, unit?: string) { return `${Number(value || 0).toFixed(2)}${unit ? ` ${unit}` : ''}` }
function rechargeText(summary: DailyReportSummary) {
  return Object.entries(summary.recharge_amounts || {}).sort(([a], [b]) => a.localeCompare(b)).map(([currency, amount]) => `${currency} ${Number(amount).toFixed(2)}`).join(' / ') || '0'
}
</script>
