<template>
  <AppLayout title="渠道使用报告" subtitle="按报告源站汇总最近窗口的渠道用量，并在首 T 超标时通知企微">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载渠道使用报告…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">渠道使用报告</h2>
            <Badge :tone="form.enabled ? 'success' : 'gray'" dot>
              {{ form.enabled ? '已启用' : '已关闭' }}
            </Badge>
          </div>
          <p class="mt-1 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
            每次执行从当前报告源站读取消费记录，按分组和渠道账号汇总；只有全站首 T 高延迟数超过触发条数才发送普通文本告警。
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
        <span>统计窗口 {{ form.lookback_hours }} 小时</span>
        <span>首 T 阈值 {{ formatThreshold(form.first_token_threshold_seconds) }}</span>
        <span v-if="config.last_error" class="flex min-w-0 items-center gap-1 text-red-600 dark:text-red-300" :title="config.last_error">
          <Icon name="exclamationTriangle" size="xs" />
          <span class="max-w-xl truncate">{{ config.last_error }}</span>
        </span>
      </div>

      <section class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary"><Icon name="clock" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ formatRelative(config.last_run_at) }}</p>
            <p class="stat-label">最近运行</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-warning"><Icon name="exclamationTriangle" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestRun?.high_latency_count ?? 0 }}</p>
            <p class="stat-label">最近高延迟记录</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-success"><Icon name="database" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ latestRun?.total_records ?? 0 }}</p>
            <p class="stat-label">最近窗口总记录</p>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-purple"><Icon name="calendar" size="lg" /></div>
          <div class="min-w-0">
            <p class="stat-value text-base">{{ nextRunLabel }}</p>
            <p class="stat-label">下一次预计执行</p>
          </div>
        </div>
      </section>

      <form class="space-y-6" @submit.prevent="save">
        <section class="card">
          <div class="card-header flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">调度与统计</h2>
              <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
                定时检查每分钟判断一次；开始和结束小时均包含，立即执行会忽略时间窗口但仍遵守报告互斥锁。企微通知在通知配置中统一管理。
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
              label="启用渠道使用报告"
              description="独立于 Guardian 自动守护开关；启用后按下面的时间窗口自动执行。"
            />
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <Field v-model="form.interval_minutes" label="运行间隔" suffix="分钟" type="number" :min="1" :max="1440" />
              <Field v-model="form.lookback_hours" label="最近统计小时数" suffix="小时" type="number" :min="1" :max="168" />
              <Field v-model="form.start_hour" label="开始小时" suffix="时" type="number" :min="0" :max="23" />
              <Field v-model="form.end_hour" label="结束小时" suffix="时" type="number" :min="0" :max="23" />
              <div class="sm:col-span-2 lg:col-span-2">
                <Field
                  v-model="form.first_token_threshold_seconds"
                  label="首 T 延迟阈值"
                  suffix="秒"
                  type="number"
                  :min="0.001"
                  :step="0.001"
                  hint="严格超过此值才计入高延迟；后端按毫秒保存。"
                />
              </div>
              <Field
                v-model="form.trigger_count"
                label="触发告警条数"
                suffix="条"
                type="number"
                :min="1"
                hint="全站高延迟数必须严格超过此值才发送企微。"
              />
              <Field
                v-model="form.timezone"
                label="时区"
                placeholder="Asia/Shanghai"
                hint="必须是 Go 可解析的时区名称。"
              />
            </div>
          </div>
        </section>

      </form>

      <section class="card">
        <div class="card-header flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">报告源站</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">选择本报告读取渠道用量的目标源站。</p>
          </div>
          <RouterLink to="/reports/source" class="btn btn-secondary btn-sm">
            <Icon name="globe" size="sm" />
            源站配置
          </RouterLink>
        </div>
        <div class="space-y-4 p-6">
          <label class="block max-w-xl">
            <span class="input-label">目标源站</span>
            <select v-model="form.source_id" class="input">
              <option v-for="item in sources" :key="item.id" :value="item.id">
                {{ item.name }} · {{ sourceTypeLabel(item.type) }}
              </option>
            </select>
          </label>
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">名称 / 类型</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ source.name }} / {{ sourceTypeLabel(source.type) }}
            </p>
          </div>
          <div class="min-w-0 rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">连接地址</p>
            <p class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">{{ source.base_url || '未配置' }}</p>
          </div>
          <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
            <p class="text-xs text-gray-500 dark:text-dark-400">配置状态</p>
            <div class="mt-1 flex items-center gap-2">
              <Badge :tone="source.configured ? 'success' : 'danger'" dot>
                {{ source.configured ? '已配置，可读取报告数据' : '未完成，请先配置源站' }}
              </Badge>
              <RouterLink v-if="!source.configured" to="/reports/source" class="text-xs text-primary-600 hover:underline dark:text-primary-300">
                前往配置
              </RouterLink>
            </div>
          </div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">最近 7 天运行记录</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">每次定时或手动执行都会保留状态、窗口、通知结果和完整聚合结果。</p>
          </div>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="refreshing" @click="refreshHistory">
            <Icon name="refresh" size="sm" />
            {{ refreshing ? '刷新中…' : '刷新' }}
          </button>
        </div>
        <div v-if="!runs.length" class="p-6">
          <EmptyState icon="calendar" title="暂无运行记录" description="保存配置后，可以先用“立即执行”验证报告链路。" />
        </div>
        <div v-else class="table-wrapper">
          <table class="table min-w-[760px]">
            <thead>
              <tr>
                <th>执行时间</th>
                <th>状态</th>
                <th>统计窗口</th>
                <th>记录 / 高延迟</th>
                <th>企微投递</th>
                <th class="w-28">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="run in runs" :key="run.id">
                <td class="whitespace-nowrap text-xs">{{ formatTime(run.started_at) }}</td>
                <td>
                  <Badge :tone="runStatusMeta(run.status).tone" dot>{{ runStatusMeta(run.status).label }}</Badge>
                  <p v-if="run.message" class="mt-1 max-w-40 truncate text-xs text-gray-500 dark:text-dark-400" :title="run.message">{{ run.message }}</p>
                </td>
                <td class="text-xs text-gray-500 dark:text-dark-400">
                  <span>{{ formatTime(run.window_start) }}</span>
                  <span class="mx-1 text-gray-300 dark:text-dark-600">至</span>
                  <span>{{ formatTime(run.window_end) }}</span>
                </td>
                <td class="whitespace-nowrap text-xs tabular-nums">
                  {{ run.total_records }} / <strong :class="run.high_latency_count ? 'text-amber-600 dark:text-amber-300' : 'text-gray-900 dark:text-white'">{{ run.high_latency_count }}</strong>
                </td>
                <td>
                  <Badge :tone="notificationMeta(run.notification_status).tone">{{ notificationMeta(run.notification_status).label }}</Badge>
                  <p v-if="run.notification_error" class="mt-1 max-w-44 truncate text-xs text-red-500" :title="run.notification_error">{{ run.notification_error }}</p>
                </td>
                <td>
                  <button type="button" class="btn btn-ghost btn-sm" @click="openRun(run)">
                    <Icon name="eye" size="sm" />
                    查看聚合
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
      title="运行聚合结果"
      :subtitle="selectedRun ? `${formatTime(selectedRun.started_at)} · ${runStatusMeta(selectedRun.status).label}` : ''"
      @close="selectedRun = null"
    >
      <template v-if="selectedRun">
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">总记录</p><p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ selectedRun.total_records }}</p></div>
          <div class="rounded-lg bg-amber-50 p-3 dark:bg-amber-900/20"><p class="text-xs text-amber-700 dark:text-amber-300">高延迟</p><p class="mt-1 text-lg font-semibold text-amber-800 dark:text-amber-200">{{ selectedRun.high_latency_count }}</p></div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">窗口起点</p><p class="mt-1 text-xs font-medium text-gray-900 dark:text-white">{{ formatTime(selectedRun.window_start) }}</p></div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900/60"><p class="text-xs text-gray-500 dark:text-dark-400">窗口终点</p><p class="mt-1 text-xs font-medium text-gray-900 dark:text-white">{{ formatTime(selectedRun.window_end) }}</p></div>
        </div>
        <div v-if="selectedRun.error" class="mt-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ selectedRun.error }}</div>
        <div class="mt-5 table-wrapper max-h-[52vh]">
          <table class="table min-w-[520px]">
            <thead><tr><th>分组</th><th>账号</th><th>高延迟</th><th>总记录</th><th>占比</th></tr></thead>
            <tbody>
              <tr v-for="row in selectedRun.summary || []" :key="`${row.group_name}-${row.account_name}`">
                <td class="max-w-48 truncate" :title="row.group_name">{{ row.group_name }}</td>
                <td class="max-w-48 truncate" :title="row.account_name">{{ row.account_name }}</td>
                <td class="tabular-nums" :class="row.high_latency_count ? 'font-semibold text-amber-700 dark:text-amber-300' : ''">{{ row.high_latency_count }}</td>
                <td class="tabular-nums">{{ row.total_records }}</td>
                <td class="tabular-nums text-gray-500 dark:text-dark-400">{{ ratio(row.high_latency_count, row.total_records) }}</td>
              </tr>
            </tbody>
          </table>
          <p v-if="!selectedRun.summary?.length" class="p-6 text-center text-sm text-gray-500 dark:text-dark-400">本次窗口没有可统计的消费记录。</p>
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
import { formatRelative, formatTime } from '@/lib/format'
import { useUIStore } from '@/stores/ui'
import type {
  ChannelUsageReportRun,
  ChannelUsageReportSaveInput,
  ChannelUsageReportView
} from '@/lib/types'

type ReportForm = Omit<ChannelUsageReportSaveInput, 'first_token_threshold_ms'> & {
  first_token_threshold_seconds: number
}

const ui = useUIStore()
const loading = ref(true)
const saving = ref(false)
const running = ref(false)
const refreshing = ref(false)
const error = ref('')
const view = ref<ChannelUsageReportView | null>(null)
const runs = ref<ChannelUsageReportRun[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const selectedRun = ref<ChannelUsageReportRun | null>(null)

const form = ref<ReportForm>({
  source_id: 'global',
  enabled: false,
  interval_minutes: 60,
  start_hour: 9,
  end_hour: 22,
  timezone: 'Asia/Shanghai',
  lookback_hours: 1,
  first_token_threshold_seconds: 30,
  trigger_count: 20
})

const config = computed(() => view.value?.config ?? {
  source_id: form.value.source_id,
  enabled: form.value.enabled,
  interval_minutes: form.value.interval_minutes,
  start_hour: form.value.start_hour,
  end_hour: form.value.end_hour,
  timezone: form.value.timezone,
  lookback_hours: form.value.lookback_hours,
  first_token_threshold_ms: Math.round(form.value.first_token_threshold_seconds * 1000),
  trigger_count: form.value.trigger_count,
  last_run_at: '', last_status: 'never', last_error: '', next_run_at: ''
})
const sources = computed(() => view.value?.sources ?? [])
const source = computed(() => sources.value.find(item => item.id === form.value.source_id) ?? view.value?.source ?? {
  id: form.value.source_id,
  name: '未选择',
  mode: 'global' as const,
  type: 'sub2api' as const,
  configured: false,
  base_url: ''
})
const latestRun = computed(() => view.value?.latest_run ?? runs.value[0] ?? null)
const busy = computed(() => saving.value || running.value || refreshing.value)
const nextRunLabel = computed(() => config.value.next_run_at ? formatTime(config.value.next_run_at) : '待启用')

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    const report = await api.channelUsageReport()
    view.value = report
    applyView(report)
    await loadRuns(1)
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function applyView(report: ChannelUsageReportView) {
  form.value = {
    source_id: report.config.source_id,
    enabled: report.config.enabled,
    interval_minutes: report.config.interval_minutes,
    start_hour: report.config.start_hour,
    end_hour: report.config.end_hour,
    timezone: report.config.timezone,
    lookback_hours: report.config.lookback_hours,
    first_token_threshold_seconds: report.config.first_token_threshold_ms / 1000,
    trigger_count: report.config.trigger_count
  }
}

async function loadRuns(nextPage: number) {
  const result = await api.channelUsageReportRuns(nextPage, pageSize)
  runs.value = result.items
  total.value = result.total
  page.value = result.page
}

async function refreshHistory() {
  refreshing.value = true
  error.value = ''
  try {
    const [report] = await Promise.all([api.channelUsageReport(), loadRuns(page.value)])
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
    const payload: ChannelUsageReportSaveInput = {
      source_id: form.value.source_id,
      enabled: form.value.enabled,
      interval_minutes: Number(form.value.interval_minutes),
      start_hour: Number(form.value.start_hour),
      end_hour: Number(form.value.end_hour),
      timezone: form.value.timezone,
      lookback_hours: Number(form.value.lookback_hours),
      first_token_threshold_ms: Math.max(1, Math.round(Number(form.value.first_token_threshold_seconds) * 1000)),
      trigger_count: Number(form.value.trigger_count)
    }
    const report = await api.saveChannelUsageReport(payload)
    view.value = report
    applyView(report)
    ui.notify('success', '渠道使用报告配置已保存')
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
    const result = await api.runChannelUsageReport()
    const run = result.run
    ui.notify(run.status === 'error' ? 'error' : run.status === 'alert' ? 'warning' : 'success', run.message || '渠道使用报告执行完成')
    await refreshHistory()
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    running.value = false
  }
}

function openRun(run: ChannelUsageReportRun) {
  selectedRun.value = run
}

function runStatusMeta(status: string) {
  if (status === 'alert') return { label: '告警', tone: 'warning' }
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

function ratio(numerator: number, denominator: number) {
  if (!denominator) return '—'
  return `${((numerator / denominator) * 100).toFixed(1)}%`
}

function formatThreshold(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  return `${seconds % 1 === 0 ? seconds.toFixed(0) : seconds.toFixed(3)} 秒`
}

function sourceTypeLabel(type: string) {
  return type === 'newapi' ? 'New API' : 'Sub2API'
}
</script>
