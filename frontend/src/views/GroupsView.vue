<template>
  <AppLayout title="分组调度" subtitle="每个分组独立选择价格优先 / 速度优先 / 均衡，并单独设置保底池">
    <div class="flex flex-wrap items-center gap-3">
      <SegmentedControl
        :model-value="filter"
        :options="filterOptions"
        @update:model-value="filter = $event as 'managed' | 'all'"
      />
      <span class="text-sm text-gray-500 dark:text-dark-400">
        共 {{ visibleGroups.length }} 个分组
      </span>
      <!--
        两个页签结果相同时明确说出来。默认 managed_group_mode=all 表示所有分组
        都参与守护，此时切换页签列表不会有任何变化，看起来像「点了没反应」。
      -->
      <span
        v-if="managedCount === guardian.groups.length && guardian.groups.length > 0"
        class="text-xs text-gray-400 dark:text-dark-500"
      >
        当前所有分组都参与守护，两个页签结果相同 ——
        <RouterLink to="/policy?tab=scope" class="text-primary-600 hover:underline dark:text-primary-400">
          去「守护范围」只勾选部分分组
        </RouterLink>
      </span>
      <div class="flex-1" />
      <span class="text-xs text-gray-400 dark:text-dark-500">
        上次同步 {{ formatRelative(guardian.status?.last_run_at) }}
      </span>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="guardian.busy"
        title="立即从 sub2api 重新拉取分组与账号"
        @click="syncNow"
      >
        <Icon name="sync" size="sm" />
        同步分组
      </button>
    </div>

    <EmptyState
      v-if="!visibleGroups.length"
      icon="grid"
      title="没有可展示的分组"
      description="先同步 sub2api，再在策略配置里选择参与守护的分组。"
    />

    <section
      v-for="group in visibleGroups"
      :id="`group-${group.id}`"
      :key="group.id"
      class="card"
      :class="focusId === group.id && 'ring-2 ring-primary-500/40'"
    >
      <div class="card-header flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h2 class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ group.name }}</h2>
            <Badge :tone="groupStatusMeta(group.state.status).tone" dot>
              {{ groupStatusMeta(group.state.status).label }}
            </Badge>
            <Badge v-if="group.excluded" tone="danger">已排除</Badge>
            <Badge v-else-if="!group.managed" tone="gray">未参与</Badge>
            <Badge v-if="group.override" tone="purple">已覆盖全局</Badge>
            <Badge v-if="!group.excluded" :tone="probeMeta(group).tone">
              {{ probeMeta(group).label }}
            </Badge>
          </div>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
            #{{ group.id }} · {{ group.platform || '全平台' }} · 分组倍率 {{ group.rate_multiplier }} ·
            渠道 {{ group.state.total_accounts }} · 权重合计 {{ group.state.total_weight.toFixed(0) }}
          </p>
          <p v-if="group.state.last_alert_message" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
            最近告警：{{ group.state.last_alert_message }}（{{ formatRelative(group.state.last_alert_at) }}）
          </p>
        </div>

        <div class="flex items-center gap-2">
          <button
            v-if="group.override"
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="guardian.busy"
            @click="clearOverride(group)"
          >
            <Icon name="refresh" size="sm" />
            回落到全局
          </button>
          <button
            type="button"
            class="btn btn-sm"
            :class="group.excluded ? 'btn-success' : 'btn-ghost'"
            :disabled="guardian.busy"
            :title="group.excluded ? '移回调度系统管控' : '整组移出调度系统管控'"
            @click="toggleExcluded(group)"
          >
            <Icon :name="group.excluded ? 'play' : 'ban'" size="sm" />
            {{ group.excluded ? '恢复管控' : '排除分组' }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="group.excluded"
            @click="openEditor(group)"
          >
            <Icon name="cog" size="sm" />
            分组策略
          </button>
        </div>
      </div>

      <div class="space-y-4 p-6">
        <div class="flex flex-wrap items-center gap-4">
          <div>
            <p class="mb-1.5 text-xs font-medium text-gray-500 dark:text-dark-400">调度策略</p>
            <SegmentedControl
              :model-value="group.strategy"
              :options="strategyOptions"
              @update:model-value="setStrategy(group, $event)"
            />
          </div>
          <div class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">健康 / 可用</p>
              <p
                class="text-sm font-semibold text-gray-900 dark:text-white"
                title="健康 = 此刻正常服务的渠道数；可用 = 仍在池子里能接流量的（含限流与降级），后者是保底判定的口径"
              >
                {{ group.state.healthy_accounts }} / {{ group.state.available_accounts }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">最高分</p>
              <p class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ group.state.best_score.toFixed(0) }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">平均分</p>
              <p class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ group.state.avg_score.toFixed(0) }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">熔断 / 暂停 / 不可用</p>
              <p
                class="text-sm font-semibold text-gray-900 dark:text-white"
                title="不可用 = 人工排除 + sub2api 侧停用 + 被关掉调用（探测正常但接不到流量）"
              >
                {{ group.state.fused_accounts }} / {{ group.state.paused_accounts }} /
                {{ group.state.excluded_accounts }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">限流 / 待探测</p>
              <p class="text-sm font-semibold">
                <span
                  :class="
                    group.state.rate_limited_accounts > 0
                      ? 'text-primary-600 dark:text-primary-400'
                      : 'text-gray-900 dark:text-white'
                  "
                  title="限流渠道仍在池子里接流量，等上游窗口重置会自动恢复，不需要人介入"
                >
                  {{ group.state.rate_limited_accounts }}
                </span>
                <span class="text-gray-900 dark:text-white">
                  / {{ group.state.pending_accounts }}
                </span>
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">分配并发</p>
              <p class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ group.state.total_concurrency }}
              </p>
            </div>
          </div>
        </div>

        <div>
          <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
            <p class="text-xs font-medium text-gray-500 dark:text-dark-400">组内权重分配</p>
            <span v-if="channelsOf(group).length" class="text-xs text-gray-400 dark:text-dark-500">
              共 {{ channelsOf(group).length }} 个渠道
            </span>
          </div>
          <EmptyState
            v-if="!channelsOf(group).length && group.excluded"
            icon="ban"
            title="该分组已移出调度系统管控"
            description="组内渠道不再参与探测与调度，也不会出现在渠道池。点击上方「恢复管控」重新纳入。"
          />
          <EmptyState v-else-if="!channelsOf(group).length" icon="server" title="该分组下没有渠道" />
          <ul v-else class="space-y-2">
            <li
              v-for="channel in pagedChannels(group)"
              :key="channel.id"
              class="flex flex-wrap items-center gap-3 rounded-xl border border-gray-100 px-3 py-2 dark:border-dark-700"
            >
              <ScoreRing :score="channel.health_score" :sample-count="channel.sample_count" />

              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ channel.name }}</p>
                  <Badge :tone="healthMeta(channel.health).tone">{{ healthMeta(channel.health).label }}</Badge>
                </div>
                <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                  首字 P95 {{ formatMs(channel.ttfb_p95_ms) }} ·
                  倍率 {{ formatMultiplier(channel.multiplier) }} ·
                  优先级 {{ channel.priority }}
                  <span v-if="channel.desired_priority && channel.desired_priority !== channel.priority">
                    → {{ channel.desired_priority }}
                  </span>
                </p>
              </div>

              <div class="w-full sm:w-56">
                <div class="progress">
                  <div class="progress-bar" :style="{ width: `${weightRatio(group, channel)}%` }" />
                </div>
                <p class="mt-1 text-right text-[11px] text-gray-500 dark:text-dark-400">
                  权重 {{ channel.weight.toFixed(0) }}
                </p>
              </div>
            </li>
          </ul>

          <MiniPager
            v-if="channelsOf(group).length"
            class="mt-3"
            :page="pageOf(group.id)"
            :page-size="pageSize"
            :total="channelsOf(group).length"
            @update:page="setPage(group.id, $event)"
          />
        </div>
      </div>
    </section>

    <Modal
      :open="editor.open"
      :title="`分组策略 · ${editor.name}`"
      subtitle="留空的项沿用全局策略；这里的设置只影响当前分组。"
      @close="editor.open = false"
    >
      <div class="space-y-4">
        <SwitchRow
          v-model="editor.enabled"
          label="参与守护"
          description="关闭后该分组不再探测、不熔断、不调权。"
        />
        <div>
          <p class="input-label">调度策略</p>
          <SegmentedControl v-model="editor.strategy" :options="strategyOptions" />
        </div>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            v-model="editor.minPoolSize"
            label="保底可用渠道数"
            type="number"
            :min="0"
            hint="熔断后可用渠道不得低于该数量，否则改为保底强留"
          />
          <Field
            v-model="editor.weightBudget"
            label="权重预算"
            type="number"
            :min="1"
            hint="该分组内可分配的权重点数总量"
          />
        </div>
        <Field
          v-if="editor.strategy === 'balanced'"
          v-model="editor.balancedPriceRatio"
          label="均衡策略中价格占比"
          type="number"
          :min="0"
          :max="1"
          :step="0.05"
          hint="0 表示完全看速度，1 表示完全看价格"
        />
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <SwitchRow v-model="editor.breakerEnabled" label="熔断" />
          <SwitchRow v-model="editor.recoveryEnabled" label="健康回池" />
          <SwitchRow v-model="editor.weightsEnabled" label="负载因子调权" />
          <SwitchRow v-model="editor.scalingEnabled" label="智能扩容" />
        </div>

        <div class="space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <SwitchRow
            v-model="editor.probeEnabled"
            label="定时测试"
            description="按下面的间隔对该分组的渠道做主动测活；关闭后该分组只依赖真实流量样本"
          />
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              v-model="editor.probeIntervalSeconds"
              label="测试间隔"
              suffix="秒"
              type="number"
              :min="30"
              :step="30"
              :disabled="!editor.probeEnabled"
              :hint="`全局默认 ${guardian.policy?.probe.interval_seconds ?? 300} 秒`"
            />
            <Field
              v-model="editor.probeModel"
              label="测试模型"
              :disabled="!editor.probeEnabled"
              placeholder="留空则用全局默认"
              hint="该分组统一使用的测活模型"
            />
          </div>
        </div>
      </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="editor.open = false">取消</button>
        <button type="button" class="btn btn-primary" :disabled="guardian.busy" @click="saveOverride">
          保存分组策略
        </button>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import Modal from '@/components/Modal.vue'
import Field from '@/components/Field.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import ScoreRing from '@/components/ScoreRing.vue'
import MiniPager from '@/components/MiniPager.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import {
  formatMultiplier,
  formatMs,
  formatRelative,
  groupStatusMeta,
  healthMeta,
  strategyLabel
} from '@/lib/format'
import type { Channel, Group, Strategy } from '@/lib/types'

const guardian = useGuardianStore()
const ui = useUIStore()
const route = useRoute()

const filter = ref<'managed' | 'all'>('managed')
const focusId = ref<number | null>(null)

/** 每组渠道列表的每页条数。 */
const pageSize = 8

/**
 * 各分组当前页码，按分组 ID 索引。
 *
 * 页面每 20 秒会静默刷新一次，页码存在这里才不会被刷回第一页。
 */
const pages = ref<Record<number, number>>({})

function pageOf(groupID: number): number {
  return pages.value[groupID] ?? 1
}

function setPage(groupID: number, page: number) {
  pages.value = { ...pages.value, [groupID]: page }
}

/**
 * channelsOf 兜底取分组的渠道列表。
 *
 * 后端已保证空分组回 `[]` 而不是 `null`，这里再挡一层：
 * 模板里有近十处 `.length`，任何一处读到 null 都会让整个页面白屏，
 * 代价与收益完全不成比例。
 */
function channelsOf(group: Group): Channel[] {
  return group.channels ?? []
}

/** pagedChannels 返回当前页的渠道。 */
function pagedChannels(group: Group): Channel[] {
  const start = (pageOf(group.id) - 1) * pageSize
  return channelsOf(group).slice(start, start + pageSize)
}


const strategyOptions = [
  { value: 'price', label: '价格优先', icon: 'dollar' as const },
  { value: 'speed', label: '速度优先', icon: 'bolt' as const },
  { value: 'balanced', label: '均衡', icon: 'swap' as const }
]

const managedCount = computed(() => guardian.groups.filter(group => group.managed).length)

/**
 * 页签带上数量，切换前就能看出会不会有变化。
 *
 * 默认 managed_group_mode=all，所有分组都参与守护，两个页签内容完全一样；
 * 不标数量的话，点击只有高亮变化，很容易被当成「按钮没反应」。
 */
const filterOptions = computed(() => [
  { value: 'managed', label: `参与守护 (${managedCount.value})` },
  { value: 'all', label: `全部分组 (${guardian.groups.length})` }
])

const visibleGroups = computed(() =>
  filter.value === 'all' ? guardian.groups : guardian.groups.filter(group => group.managed)
)

/**
 * 渠道数量会随熔断、删除和筛选变化，页码可能越界——例如停在第 3 页时
 * 渠道被删到只剩一页。这里把越界页码收敛回最后一页，避免出现空白列表。
 */
watch(
  () => visibleGroups.value.map(group => `${group.id}:${channelsOf(group).length}`).join(','),
  () => {
    const next: Record<number, number> = {}
    let changed = false

    for (const group of visibleGroups.value) {
      const current = pageOf(group.id)
      const totalPages = Math.max(1, Math.ceil(channelsOf(group).length / pageSize))
      const clamped = Math.min(current, totalPages)
      if (clamped !== 1) {
        next[group.id] = clamped
      }
      if (clamped !== current) {
        changed = true
      }
    }

    // 顺带丢弃已经不在列表里的分组，避免 map 无限增长。
    if (changed || Object.keys(next).length !== Object.keys(pages.value).length) {
      pages.value = next
    }
  }
)

const editor = reactive({
  open: false,
  id: 0,
  name: '',
  enabled: true,
  strategy: 'price' as Strategy,
  minPoolSize: 1,
  weightBudget: 400,
  balancedPriceRatio: 0.5,
  breakerEnabled: true,
  recoveryEnabled: true,
  weightsEnabled: true,
  scalingEnabled: false,
  probeEnabled: true,
  probeIntervalSeconds: 300,
  probeModel: ''
})

onMounted(() => {
  const focus = Number(route.query.focus)
  if (!Number.isNaN(focus) && focus > 0) {
    focusId.value = focus
    filter.value = 'all'
    window.setTimeout(() => {
      document.getElementById(`group-${focus}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 200)
  }
})

// probeMeta 返回该分组生效的定时测试节奏，未覆盖时显示全局值。
function probeMeta(group: Group): { label: string; tone: string } {
  const enabled = group.override?.probe_enabled ?? guardian.policy?.probe.enabled ?? true
  if (!enabled) {
    return { label: '定时测试已关', tone: 'gray' }
  }
  const seconds =
    group.override?.probe_interval_seconds ?? guardian.policy?.probe.interval_seconds ?? 300
  return { label: `每 ${formatInterval(seconds)} 测活`, tone: 'primary' }
}

function formatInterval(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${Math.round(seconds / 60)} 分钟`
}

function weightRatio(group: Group, channel: Channel): number {
  const total = channelsOf(group).reduce((sum, item) => sum + Math.max(item.weight, 0), 0)
  if (total <= 0) return 0
  return Math.round((Math.max(channel.weight, 0) / total) * 100)
}

function openEditor(group: Group) {
  const override = group.override ?? {}
  const policy = guardian.policy
  editor.open = true
  editor.id = group.id
  editor.name = group.name
  editor.enabled = override.enabled ?? group.managed
  editor.strategy = (override.strategy ?? group.strategy) as Strategy
  editor.minPoolSize = override.min_pool_size ?? policy?.breaker.min_pool_size ?? 1
  editor.weightBudget = override.weight_budget ?? policy?.weights.budget ?? 400
  editor.balancedPriceRatio =
    override.balanced_price_ratio ?? policy?.weights.balanced_price_ratio ?? 0.5
  editor.breakerEnabled = override.breaker_enabled ?? policy?.breaker.enabled ?? true
  editor.recoveryEnabled = override.recovery_enabled ?? policy?.recovery.enabled ?? true
  editor.weightsEnabled = override.weights_enabled ?? policy?.weights.enabled ?? true
  editor.scalingEnabled = override.scaling_enabled ?? policy?.scaling.enabled ?? false
  editor.probeEnabled = override.probe_enabled ?? policy?.probe.enabled ?? true
  editor.probeIntervalSeconds =
    override.probe_interval_seconds ?? policy?.probe.interval_seconds ?? 300
  editor.probeModel = override.probe_model ?? ''
}

async function saveOverride() {
  try {
    await guardian.run(() =>
      api.saveGroupPolicy(editor.id, {
        enabled: editor.enabled,
        strategy: editor.strategy,
        min_pool_size: editor.minPoolSize,
        weight_budget: editor.weightBudget,
        balanced_price_ratio: editor.balancedPriceRatio,
        breaker_enabled: editor.breakerEnabled,
        recovery_enabled: editor.recoveryEnabled,
        weights_enabled: editor.weightsEnabled,
        scaling_enabled: editor.scalingEnabled,
        probe_enabled: editor.probeEnabled,
        probe_interval_seconds: editor.probeIntervalSeconds,
        // 留空表示沿用全局模型，不要写一个空字符串把它锁死。
        probe_model: editor.probeModel.trim() || undefined
      })
    )
    editor.open = false
    ui.notify('success', `分组「${editor.name}」策略已保存`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function setStrategy(group: Group, strategy: string) {
  if (strategy === group.strategy) return
  try {
    await guardian.run(() =>
      api.saveGroupPolicy(group.id, { ...(group.override ?? {}), strategy })
    )
    ui.notify('success', `分组「${group.name}」已切换为${strategyLabel(strategy)}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function toggleExcluded(group: Group) {
  const next = !group.excluded
  if (
    next &&
    !window.confirm(
      `确定要把分组「${group.name}」移出调度系统管控吗？\n\n` +
        '该分组下的渠道将不再被探测、熔断或调权，现有配置保持不动。\n' +
        '只属于这个分组的渠道也会从「渠道池」中隐藏（同时属于其他分组的仍然可见）。'
    )
  ) {
    return
  }
  try {
    await guardian.run(() => api.excludeGroup(group.id, next))
    await guardian.loadPolicy()
    ui.notify('success', `分组「${group.name}」${next ? '已移出管控' : '已恢复管控'}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function syncNow() {
  try {
    await guardian.run(() => api.sync())
    ui.notify('success', '已从 sub2api 重新同步分组与账号')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function clearOverride(group: Group) {
  try {
    await guardian.run(() => api.clearGroupPolicy(group.id))
    ui.notify('success', `分组「${group.name}」已回落到全局策略`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

onMounted(async () => {
  if (!guardian.policy) {
    try {
      await guardian.loadPolicy()
    } catch {
      // 策略加载失败时编辑器会用后端默认值兜底。
    }
  }
})
</script>
