<template>
  <AppLayout title="降智检测" subtitle="定期检查渠道模型指纹，按渠道与模型追踪变化">
    <div v-if="loading" class="card p-8 text-center text-gray-500">正在读取检测配置…</div>
    <div v-else-if="loadError" class="card p-6">
      <p class="text-red-600">{{ loadError }}</p>
      <button class="btn btn-secondary mt-3" @click="load">重试</button>
    </div>
    <template v-else>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <SegmentedControl v-model="activeTab" :options="[{ value: 'overview', label: '检测总览' }, { value: 'settings', label: '检测配置' }]" />
        <p class="text-xs text-gray-500"><span v-if="dirty" class="mr-3 text-amber-600">配置有未保存更改</span>{{ running ? '正在检测 · 结果自动刷新' : '结果每 5 秒自动刷新' }}</p>
      </div>

      <div v-show="activeTab === 'overview'" class="space-y-5">
        <section class="card p-4">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <Badge :tone="savedSettings.enabled ? 'success' : 'gray'">{{ savedSettings.enabled ? '定期检测已开启' : '定期检测已关闭' }}</Badge>
              <span class="text-gray-600 dark:text-dark-300">每 {{ savedSettings.interval_minutes }} 分钟</span>
              <span class="text-gray-400">·</span>
              <span class="text-gray-500">{{ savedSettings.quiet_enabled ? `${savedSettings.quiet_start}–${savedSettings.quiet_end} 免检` : '全天检测' }} · {{ savedSettings.timezone }}</span>
            </div>
            <button class="btn btn-secondary btn-sm" :disabled="overviewLoading" @click="refreshOverview()">{{ overviewLoading ? '刷新中…' : '刷新结果' }}</button>
          </div>
          <div class="mt-4 flex flex-wrap gap-3">
            <select v-model.number="overviewGroup" class="input w-auto" aria-label="总览渠道池分组"><option :value="0">全部渠道池分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select>
            <input v-model="overviewSearch" class="input min-w-0 flex-1 sm:max-w-sm" placeholder="搜索渠道或模型" aria-label="搜索时间线" />
          </div>
          <div class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-gray-500">
            <span v-for="status in legendStatuses" :key="status" class="inline-flex items-center gap-1.5"><span class="h-2.5 w-2.5 rounded-sm" :class="statusCell(status)" />{{ statusLabel(status) }}</span>
            <span class="sm:ml-auto">每个模型最近 24 次 · 旧 → 新 · 点击查看详情</span>
          </div>
        </section>
        <p v-if="overviewError" role="alert" class="text-sm text-red-600">{{ overviewError }}</p>
        <div v-if="!timelineGroups.length" class="card p-10 text-center">
          <p class="text-sm text-gray-500">{{ overviewLoading ? '正在读取各模型时间线…' : overviewSearch || overviewGroup ? '没有匹配的渠道或模型' : '还没有检测项，先选择需要监测的渠道和模型。' }}</p>
          <button v-if="!overviewSearch && !overviewGroup" class="btn btn-primary mt-4" @click="activeTab = 'settings'">配置检测项</button>
        </div>
        <section v-for="channel in timelineGroups" :key="channel.id" class="card">
          <div class="card-header flex flex-wrap items-center justify-between gap-2">
            <div class="flex min-w-0 items-center gap-2"><Icon name="server" size="sm" /><h2 class="break-words font-semibold text-gray-900 dark:text-white">{{ channel.name }}</h2><span class="text-xs text-gray-400">#{{ channel.id }}</span></div>
            <span class="text-xs text-gray-500">{{ channel.rows.length }} 个模型</span>
          </div>
          <div v-for="row in channel.rows" :key="targetKey(row.target)" class="grid gap-3 border-b border-gray-100 p-4 last:border-0 dark:border-dark-700 lg:grid-cols-[minmax(160px,1fr)_minmax(0,2fr)_auto] lg:items-center lg:gap-5">
            <div class="min-w-0">
              <p class="break-all text-sm font-semibold text-gray-900 dark:text-white">{{ row.target.model }}</p>
              <p class="mt-1 truncate text-xs text-gray-500" :title="row.target.expected_model">预期：{{ row.target.expected_model || '仅归因' }}<span v-if="!row.configured"> · 已移出计划</span></p>
              <div class="mt-2"><Badge :tone="row.latest ? statusTone(row.latest.status) : 'gray'">{{ row.latest ? statusLabel(row.latest.status) : '等待首次检测' }}</Badge></div>
            </div>
            <div class="min-w-0">
              <div v-if="row.runs.length" class="flex items-center gap-1" :aria-label="`${channel.name} ${row.target.model} 最近检测时间线`">
                <button v-for="run in row.runs" :key="run.id" type="button" class="h-8 min-w-0 flex-1 rounded transition hover:opacity-75 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500" :class="statusCell(run.status)" :title="runTooltip(run)" :aria-label="runTooltip(run)" @click="showTimeline(row.target, run)" />
              </div>
              <div v-else class="flex h-8 items-center rounded border border-dashed border-gray-200 px-3 text-xs text-gray-400 dark:border-dark-600">暂无记录，等待计划执行或立即检测</div>
              <div v-if="row.runs.length" class="mt-1.5 flex justify-between gap-2 text-[11px] text-gray-400"><time>{{ shortTimestamp(row.runs[0]!.started_at) }}</time><time>最新 {{ shortTimestamp(row.latest!.started_at) }}</time></div>
              <p v-if="row.latest" class="mt-2 truncate text-xs text-gray-500" :title="row.latest.message">{{ row.latest.prediction ? `最近识别：${row.latest.prediction} · ${percent(row.latest.probability)}` : row.latest.message || '正在检测…' }}</p>
            </div>
            <div class="flex gap-2 lg:flex-col">
              <button class="btn btn-secondary btn-sm" @click="showTimeline(row.target)">查看记录</button>
              <button v-if="row.configured" class="btn btn-ghost btn-sm" :disabled="running || starting || dirty" :title="dirty ? '请先保存配置' : '立即发送三次挑战'" @click="runNow(row.target)">立即检测</button>
            </div>
          </div>
        </section>
        <p class="text-xs text-gray-400">颜色表示每次检测的结论，时间线按检测次数排列。模型指纹归因仅供参考，不能直接证明智力下降。</p>
      </div>

      <div v-show="activeTab === 'settings'" class="space-y-6">
      <div class="rounded-xl border border-primary-100 bg-primary-50 p-4 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-300">
        参考 <a class="text-primary-600 underline dark:text-primary-400" href="https://github.com/xqy2006/ModelTrace" target="_blank" rel="noopener noreferrer">ModelTrace</a>，每轮发送 3 次独立数字挑战，会消耗渠道额度。结果是候选库内的模型归因，仅供参考；未收录的模型也会被归到最相近的候选。
      </div>
      <section class="card">
        <div class="card-header flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">检测计划</h2>
            <p class="mt-1 text-xs text-gray-500">{{ running ? '正在检测，结果自动刷新' : '后台按计划运行，关闭页面后仍继续' }} · 独立于渠道守护调度开关</p>
          </div>
          <label class="flex items-center gap-2 text-sm"><Toggle v-model="settings.enabled" aria-label="启用定期检测" />启用定期检测</label>
        </div>
        <form class="space-y-5 p-5" @submit.prevent="save">
          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Field v-model="settings.interval_minutes" label="检测间隔" type="number" suffix="分钟" :min="1" :max="10080" hint="默认 60 分钟（1 小时）" />
            <label class="block"><span class="input-label">时区</span><input v-model="settings.timezone" class="input" required placeholder="Asia/Shanghai" /><span class="input-hint">默认北京时间，免检时段按此时区执行</span></label>
            <label class="block"><span class="input-label">免检开始</span><input v-model="settings.quiet_start" class="input" type="time" :disabled="!settings.quiet_enabled" required /></label>
            <label class="block"><span class="input-label">免检结束</span><input v-model="settings.quiet_end" class="input" type="time" :disabled="!settings.quiet_enabled" required /></label>
          </div>
          <div class="flex flex-wrap items-center gap-4">
            <label class="flex items-center gap-2 text-sm"><Toggle v-model="settings.quiet_enabled" aria-label="启用免检时段" />启用免检时段</label>
            <span class="text-xs text-gray-500">支持跨午夜；进入免检时段后不再发起新的挑战。错过的轮次不补跑。</span>
          </div>
          <div class="flex items-center gap-3">
            <button class="btn btn-primary" :disabled="saving">{{ saving ? '保存中…' : '保存计划与选择' }}</button>
            <span class="text-sm text-gray-500">已选 {{ settings.targets.length }} 个渠道模型<span v-if="dirty" class="text-amber-600"> · 有未保存更改</span></span>
          </div>
        </form>
      </section>

      <section class="card">
        <div class="card-header flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold text-gray-900 dark:text-white">选择渠道与模型</h2>
          <div class="flex flex-wrap gap-2">
            <select v-model.number="groupID" class="input w-auto" aria-label="渠道池分组">
              <option :value="0">全部渠道池分组</option>
              <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
            </select>
            <input v-model="search" class="input w-auto" placeholder="搜索渠道名称" aria-label="搜索渠道" />
          </div>
        </div>
        <p class="px-5 pt-4 text-xs text-gray-500">支持 API Key 渠道的 OpenAI / Anthropic 兼容接口。检测直接请求渠道地址，请使用上游实际模型名，不使用 sub2api 的模型映射与代理。OAuth 等账号暂不支持。</p>
        <div v-if="!filteredChannels.length" class="p-8 text-center text-sm text-gray-500">没有匹配的渠道，请先同步渠道池或调整筛选条件。</div>
        <div v-for="channel in filteredChannels" :key="channel.id" class="border-b border-gray-100 p-5 last:border-0 dark:border-dark-700">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="min-w-0">
              <p class="break-words font-medium text-gray-900 dark:text-white">{{ channel.name }} <span class="text-xs text-gray-400">#{{ channel.id }}</span></p>
              <p class="mt-1 text-xs text-gray-500">{{ channel.platform }} · {{ channel.type }} · 已选 {{ selectedCount(channel.id) }} 个模型</p>
            </div>
            <button class="btn btn-secondary btn-sm" :disabled="!supported(channel) || modelLoading[channel.id]" @click="toggleModels(channel.id)">
              {{ !supported(channel) ? '暂不支持此账号类型' : modelLoading[channel.id] ? '读取模型中…' : expanded === channel.id ? '收起模型' : '选择模型' }}
            </button>
          </div>
          <div v-if="expanded === channel.id" class="mt-4 space-y-3">
            <p v-if="modelErrors[channel.id]" class="text-sm text-amber-600">{{ modelErrors[channel.id] }}</p>
            <div v-if="models[channel.id]?.length" class="flex flex-wrap items-center gap-3">
              <button class="btn btn-ghost btn-sm" @click="selectAll(channel.id)">选择全部模型</button>
              <button class="btn btn-ghost btn-sm" @click="removeChannel(channel.id)">取消此渠道全部选择</button>
              <span class="text-xs text-gray-500">{{ models[channel.id]?.length }} 个可用模型</span>
            </div>
            <div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              <label v-for="model in models[channel.id] || []" :key="model" class="flex min-w-0 items-start gap-2 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
                <input class="mt-0.5" type="checkbox" :checked="selected(channel.id, model)" @change="toggleTarget(channel.id, model)" />
                <span class="break-all">{{ model }}</span>
              </label>
            </div>
            <form class="flex flex-wrap gap-2" @submit.prevent="addCustom(channel.id)">
              <input v-model="customModels[channel.id]" class="input max-w-sm" placeholder="未列出？输入准确的请求模型名称" :aria-label="`${channel.name} 自定义模型`" />
              <button class="btn btn-secondary btn-sm">添加模型</button>
            </form>
          </div>
        </div>
      </section>

      <section v-if="settings.targets.length" class="card">
        <div class="card-header"><h2 class="font-semibold text-gray-900 dark:text-white">已选检测项</h2><p class="mt-1 text-xs text-gray-500">模型别名可手动选择预期模型。立即检测使用已保存配置，忽略定期开关和免检时段。</p></div>
        <div class="overflow-x-auto">
          <table class="table min-w-[740px]">
            <thead><tr><th>渠道 / 请求模型</th><th>预期模型（指纹库）</th><th class="text-right">操作</th></tr></thead>
            <tbody>
              <tr v-for="target in settings.targets" :key="targetKey(target)">
                <td><p class="font-medium">{{ channelName(target.account_id) }}</p><p class="mt-1 break-all text-xs text-gray-500">{{ target.model }}</p></td>
                <td><select v-model="target.expected_model" class="input" :aria-label="`${target.model} 预期模型`"><option value="">未收录 / 仅归因</option><option v-for="model in bankModels" :key="model" :value="model">{{ model }}</option></select></td>
                <td><div class="flex justify-end gap-1"><button class="btn btn-ghost btn-sm" @click="showTimeline(target)">时间线</button><button class="btn btn-secondary btn-sm" :disabled="running || starting || dirty" :title="dirty ? '请先保存配置' : '立即发送三次挑战'" @click="runNow(target)">立即检测</button><button class="btn btn-ghost btn-sm text-red-600" @click="removeTarget(target)">移除</button></div></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex items-center gap-3 border-t border-gray-100 p-4 dark:border-dark-700"><button class="btn btn-primary btn-sm" :disabled="saving || !dirty" @click="save">保存选择</button><span class="text-xs text-gray-500">移除检测项不会删除历史结果。</span></div>
      </section>

      </div>

      <Modal :open="historyOpen" :title="historyModel" :subtitle="`${channelName(historyAccount)} · 检测记录 · ${savedSettings.timezone}`" @close="closeHistory">
        <div class="mb-4 flex items-center justify-between gap-2"><p class="text-xs text-gray-500">仅显示此渠道、此模型 · 最新记录在前</p><button class="btn btn-secondary btn-sm" :disabled="historyLoading" @click="refreshHistory()">刷新记录</button></div>
        <p v-if="historyError" role="alert" class="px-5 pb-4 text-sm text-red-600">{{ historyError }}</p>
        <p v-if="!runs.length" class="px-5 pb-8 text-center text-sm text-gray-500">{{ historyLoading ? '读取中…' : '暂无检测记录。保存并启用计划，或点击某个模型的「立即检测」。' }}</p>
        <ol class="space-y-5 px-5 pb-5">
          <li v-for="run in runs" :id="`model-check-run-${run.id}`" :key="run.id" class="relative border-l-2 pl-5" :class="[statusBorder(run.status), run.id === selectedRunID && 'rounded-r-lg bg-primary-50 py-3 pr-3 dark:bg-dark-800']">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex flex-wrap items-center gap-2"><Badge :tone="statusTone(run.status)">{{ statusLabel(run.status) }}</Badge><time class="text-xs text-gray-500">{{ timestamp(run.started_at) }}</time><span v-if="run.finished_at" class="text-xs text-gray-400">{{ Math.max(0, Math.round((Date.parse(run.finished_at) - Date.parse(run.started_at)) / 1000)) }} 秒</span></div>
            </div>
            <p class="mt-2 break-all text-sm font-medium text-gray-900 dark:text-white">{{ run.account_name || `渠道 #${run.account_id}` }} · {{ run.model }}</p>
            <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ run.message || '正在发送挑战并分析指纹…' }}</p>
            <p v-if="run.prediction" class="mt-2 break-all text-sm">预期：{{ run.expected_model || '未指定' }} → 识别：<strong>{{ run.prediction }}</strong> <span class="text-gray-500">（候选库内概率 {{ percent(run.probability) }}）</span></p>
            <details v-if="run.status !== 'running'" :open="run.id === selectedRunID" class="mt-2 text-xs text-gray-500">
              <summary class="cursor-pointer">查看检测详情</summary>
              <p class="mt-2">有效回答 {{ run.valid_outputs }}/3 · 提取数字数量 {{ run.number_counts.join(' / ') || '无' }} · 指纹库 {{ run.bank_version }}</p>
              <ul v-if="run.candidates.length" class="mt-2 space-y-1"><li v-for="candidate in run.candidates.slice(0, 5)" :key="candidate.model">{{ candidate.model }}：{{ percent(candidate.probability) }}</li></ul>
            </details>
          </li>
        </ol>
        <div v-if="hasMore" class="border-t border-gray-100 p-4 text-center dark:border-dark-700"><button class="btn btn-secondary btn-sm" :disabled="historyLoading" @click="refreshHistory(true)">加载更早结果</button></div>
        <template #footer><button class="btn btn-secondary" @click="closeHistory">关闭记录</button></template>
      </Modal>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Field from '@/components/Field.vue'
import Toggle from '@/components/Toggle.vue'
import Icon from '@/components/Icon.vue'
import Modal from '@/components/Modal.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { api } from '@/lib/api'
import type { Channel, Group, ModelCheckRun, ModelCheckSettings, ModelCheckTarget, ModelCheckTimeline } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const settings = ref<ModelCheckSettings>({ enabled: false, interval_minutes: 60, quiet_enabled: true, quiet_start: '23:00', quiet_end: '08:00', timezone: 'Asia/Shanghai', targets: [] })
const saved = ref('')
const dirty = computed(() => JSON.stringify(settings.value) !== saved.value)
const savedSettings = computed<ModelCheckSettings>(() => saved.value ? JSON.parse(saved.value) : settings.value)
const activeTab = ref('overview')
const overviewGroup = ref(0)
const overviewSearch = ref('')
const overviewLoading = ref(false)
const overviewError = ref('')
const timelines = ref<ModelCheckTimeline[]>([])
const legendStatuses: ModelCheckRun['status'][] = ['consistent', 'suspected', 'inconclusive', 'error', 'running', 'unverified', 'interrupted']
const channels = ref<Channel[]>([])
const groups = ref<Group[]>([])
const bankModels = ref<string[]>([])
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const starting = ref(false)
const running = ref(false)
const groupID = ref(0)
const search = ref('')
const expanded = ref<number | null>(null)
const models = ref<Record<number, string[]>>({})
const modelLoading = ref<Record<number, boolean>>({})
const modelErrors = ref<Record<number, string>>({})
const customModels = ref<Record<number, string>>({})
const filteredChannels = computed(() => channels.value.filter(channel => (!groupID.value || channel.groups.some(g => g.id === groupID.value)) && channel.name.toLowerCase().includes(search.value.toLowerCase())))
const supported = (channel: Channel) => ['apikey', 'api_key', 'key'].includes(channel.type.trim().toLowerCase())
const selected = (id: number, model: string) => settings.value.targets.some(t => t.account_id === id && t.model === model)
const selectedCount = (id: number) => settings.value.targets.filter(t => t.account_id === id).length
const targetKey = (t: Pick<ModelCheckTarget, 'account_id' | 'model'>) => `${t.account_id}/${t.model}`
const channelName = (id: number) => channels.value.find(c => c.id === id)?.name || `渠道 #${id}（已移除）`
const runs = ref<ModelCheckRun[]>([])
const historyAccount = ref(0)
const historyModel = ref('')
const historyLoading = ref(false)
const historyError = ref('')
const hasMore = ref(false)
const historyOpen = ref(false)
const selectedRunID = ref<number | null>(null)
type TimelineRow = { target: ModelCheckTarget; configured: boolean; runs: ModelCheckRun[]; latest?: ModelCheckRun }
const timelineGroups = computed(() => {
  const rows = new Map<string, TimelineRow>()
  for (const timeline of timelines.value) {
    const latest = timeline.runs[0]
    if (!latest) continue
    rows.set(targetKey(timeline), { target: latest, configured: false, runs: [...timeline.runs].reverse(), latest })
  }
  for (const target of savedSettings.value.targets) {
    const row = rows.get(targetKey(target))
    rows.set(targetKey(target), { target, configured: true, runs: row?.runs || [], latest: row?.latest })
  }
  const result = new Map<number, { id: number; name: string; rows: TimelineRow[] }>()
  const query = overviewSearch.value.trim().toLowerCase()
  for (const row of rows.values()) {
    const channel = channels.value.find(c => c.id === row.target.account_id)
    const name = channel?.name || row.latest?.account_name || channelName(row.target.account_id)
    if (overviewGroup.value && !channel?.groups.some(g => g.id === overviewGroup.value) && row.target.group_id !== overviewGroup.value) continue
    if (query && !`${name} ${row.target.model}`.toLowerCase().includes(query)) continue
    if (!result.has(row.target.account_id)) result.set(row.target.account_id, { id: row.target.account_id, name, rows: [] })
    result.get(row.target.account_id)!.rows.push(row)
  }
  return [...result.values()].sort((a, b) => a.id - b.id).map(channel => ({ ...channel, rows: channel.rows.sort((a, b) => a.target.model.localeCompare(b.target.model)) }))
})
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let historySequence = 0
let historyFilter = ''

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [view, channelList, groupList, history] = await Promise.all([api.modelCheck(), api.channels(), api.groups(), api.modelCheckTimelines()])
    settings.value = view.settings
    saved.value = JSON.stringify(view.settings)
    bankModels.value = view.models
    running.value = view.running
    channels.value = channelList.items || []
    groups.value = groupList.items || []
    timelines.value = history.items
  } catch (error) { loadError.value = (error as Error).message }
  finally { loading.value = false }
}

async function save() {
  saving.value = true
  const snapshot = JSON.stringify(settings.value)
  try {
    await api.saveModelCheck(JSON.parse(snapshot))
    saved.value = snapshot
    ui.notify('success', '检测计划与模型选择已保存')
    await refreshOverview()
  } catch (error) { ui.notify('error', (error as Error).message) }
  finally { saving.value = false }
}

async function toggleModels(id: number) {
  if (expanded.value === id) { expanded.value = null; return }
  expanded.value = id
  if (models.value[id]) return
  modelLoading.value[id] = true
  modelErrors.value[id] = ''
  try { models.value[id] = [...new Set((await api.channelModels(id)).models || [])] }
  catch (error) { modelErrors.value[id] = `${(error as Error).message}，可手动输入模型名。` }
  finally { modelLoading.value[id] = false }
}

function addTarget(id: number, model: string) {
  if (selected(id, model)) return
  const expected = bankModels.value.includes(model) ? model : ''
  settings.value.targets.push({ account_id: id, group_id: groupID.value, model, expected_model: expected })
}
function toggleTarget(id: number, model: string) {
  if (selected(id, model)) settings.value.targets = settings.value.targets.filter(t => !(t.account_id === id && t.model === model))
  else addTarget(id, model)
}
function selectAll(id: number) { for (const model of models.value[id] || []) addTarget(id, model) }
function removeChannel(id: number) { settings.value.targets = settings.value.targets.filter(t => t.account_id !== id) }
function removeTarget(target: ModelCheckTarget) { settings.value.targets = settings.value.targets.filter(t => targetKey(t) !== targetKey(target)) }
function addCustom(id: number) {
  const model = (customModels.value[id] || '').trim()
  if (!model) return
  addTarget(id, model)
  if (!models.value[id]?.includes(model)) models.value[id] = [...(models.value[id] || []), model]
  customModels.value[id] = ''
}

async function runNow(target: ModelCheckTarget) {
  starting.value = true
  try {
    await api.runModelCheck(target)
    running.value = true
    ui.notify('success', '检测已在后台开始')
    await refreshOverview()
    await showTimeline(target)
  } catch (error) { ui.notify('error', (error as Error).message) }
  finally { starting.value = false }
}

async function refreshHistory(older = false) {
  if (!historyOpen.value) return
  const sequence = ++historySequence
  historyLoading.value = true
  historyError.value = ''
  try {
    const params: Record<string, string> = { account_id: String(historyAccount.value), model: historyModel.value.trim() }
    const filter = JSON.stringify(params)
    const append = older && filter === historyFilter
    if (append && runs.value.length) params.before = String(runs.value[runs.value.length - 1]!.id)
    const result = await api.modelCheckRuns(params)
    if (sequence !== historySequence || disposed) return
    runs.value = append ? [...runs.value, ...result.items] : result.items
    historyFilter = filter
    hasMore.value = result.items.length === 30
  } catch (error) { if (sequence === historySequence) historyError.value = (error as Error).message }
  finally { if (sequence === historySequence) historyLoading.value = false }
}
async function showTimeline(target: ModelCheckTarget, selectedRun?: ModelCheckRun) {
  historyAccount.value = target.account_id
  historyModel.value = target.model
  selectedRunID.value = selectedRun?.id || null
  runs.value = timelines.value.find(t => targetKey(t) === targetKey(target))?.runs || []
  hasMore.value = false
  historyOpen.value = true
  await refreshHistory()
  await nextTick()
  if (selectedRun && historyOpen.value) document.getElementById(`model-check-run-${selectedRun.id}`)?.scrollIntoView({ block: 'nearest' })
}
function closeHistory() { historyOpen.value = false; historySequence++; historyLoading.value = false }
async function refreshOverview() {
  if (overviewLoading.value) return
  overviewLoading.value = true
  try {
    const [view, history] = await Promise.all([api.modelCheck(), api.modelCheckTimelines()])
    if (disposed) return
    running.value = view.running
    timelines.value = history.items
    overviewError.value = ''
  } catch (error) { overviewError.value = `时间线刷新失败，当前显示上次结果：${(error as Error).message}` }
  finally { overviewLoading.value = false }
}
function timestamp(value: string) {
  try { return new Intl.DateTimeFormat('zh-CN', { timeZone: savedSettings.value.timezone, dateStyle: 'medium', timeStyle: 'medium', hour12: false }).format(new Date(value)) }
  catch { return new Date(value).toLocaleString('zh-CN') }
}
const percent = (value: number) => `${(value * 100).toFixed(1)}%`
const statusLabel = (status: ModelCheckRun['status']) => ({ running: '检测中', consistent: '指纹一致', suspected: '疑似异常', inconclusive: '证据不足', unverified: '仅归因', error: '检测失败', interrupted: '检测中断' })[status]
const statusTone = (status: ModelCheckRun['status']) => status === 'consistent' ? 'success' : status === 'suspected' ? 'danger' : status === 'running' ? 'primary' : ['error', 'interrupted', 'inconclusive'].includes(status) ? 'warning' : 'gray'
const statusBorder = (status: ModelCheckRun['status']) => status === 'consistent' ? 'border-emerald-400' : status === 'suspected' ? 'border-red-400' : status === 'running' ? 'border-blue-400' : 'border-gray-300 dark:border-dark-600'
const statusCell = (status: ModelCheckRun['status']) => ({ consistent: 'bg-emerald-500', suspected: 'bg-red-500', inconclusive: 'bg-amber-400', error: 'bg-orange-500', running: 'animate-pulse bg-blue-500', unverified: 'bg-slate-400', interrupted: 'bg-gray-300 dark:bg-dark-500' })[status]
const runTooltip = (run: ModelCheckRun) => `${run.account_name || `渠道 #${run.account_id}`} · ${run.model} · ${timestamp(run.started_at)} · ${statusLabel(run.status)}${run.prediction ? ` · ${run.prediction} ${percent(run.probability)}` : ''}`
function shortTimestamp(value: string) {
  try { return new Intl.DateTimeFormat('zh-CN', { timeZone: savedSettings.value.timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value)) }
  catch { return timestamp(value) }
}

async function poll() {
  if (disposed) return
  if (!loading.value && !loadError.value) {
    try {
      await refreshOverview()
      if (historyOpen.value && runs.value.length <= 30 && !historyLoading.value) await refreshHistory()
    } catch { /* Foreground actions display errors; polling retries without repeated toasts. */ }
  }
  if (!disposed) timer = setTimeout(poll, 5000)
}
onMounted(async () => { await load(); if (!disposed) timer = setTimeout(poll, 5000) })
onBeforeUnmount(() => { disposed = true; historySequence++; clearTimeout(timer) })
</script>
