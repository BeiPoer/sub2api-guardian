<template>
  <AppLayout title="渠道池" subtitle="健康分、首字延迟、调度倍率与权重一屏可见，可逐条调优">
    <!-- 概览计数：先看总量与各状态分布，再决定筛选什么 -->
    <div class="flex flex-wrap items-center gap-2">
      <button
        v-for="tab in statusTabs"
        :key="tab.value"
        type="button"
        class="rounded-xl border px-3 py-2 text-sm transition-colors"
        :class="
          healthFilter === tab.value
            ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
            : 'border-gray-200 bg-white text-gray-600 hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-300 dark:hover:bg-dark-700'
        "
        @click="setHealthFilter(tab.value)"
      >
        {{ tab.label }}
        <span class="ml-1 font-semibold tabular-nums">{{ tab.count }}</span>
      </button>
    </div>

    <div class="card">
      <div class="card-header flex flex-wrap items-center gap-3">
        <div class="flex flex-1 flex-wrap items-center gap-3">
          <label class="relative">
            <Icon
              name="search"
              size="sm"
              class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400"
            />
            <input
              v-model="search"
              class="input w-64 pl-9"
              placeholder="搜索渠道 / 平台 / 分组 / 模型"
            />
          </label>
          <select v-model="groupFilter" class="input w-44">
            <option value="">全部分组</option>
            <option v-for="group in guardian.groups" :key="group.id" :value="String(group.id)">
              {{ group.name }}
            </option>
          </select>
          <select v-model="typeFilter" class="input w-36">
            <option value="">全部类型</option>
            <option v-for="type in channelTypes" :key="type" :value="type">{{ type }}</option>
          </select>
          <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300">
            <Toggle v-model="managedOnly" />
            仅受管
          </label>
          <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300">
            <Toggle v-model="showExcluded" />
            显示已排除
          </label>
        </div>
        <span class="text-sm text-gray-500 dark:text-dark-400">
          筛选出 {{ filtered.length }} / 共 {{ guardian.channels.length }} 个渠道
        </span>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="syncing || guardian.busy"
          title="从 sub2api 拉取所有渠道的当前状态和调度参数，不执行测试"
          @click="syncNow"
        >
          <Icon name="sync" size="sm" :class="syncing ? 'animate-spin' : ''" />
          {{ syncing ? '同步中…' : '立即同步' }}
        </button>
      </div>

      <div class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th class="w-64">渠道</th>
              <th class="w-32">健康分</th>
              <th class="w-40">最近结果</th>
              <th class="w-28">首字延迟</th>
              <th class="w-32">调度倍率</th>
              <th class="w-32">权重</th>
              <th class="w-40">优先级 / 负载</th>
              <th class="w-36">状态</th>
              <th class="w-32 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="channel in paged"
              :key="channel.id"
              :class="channel.excluded && 'opacity-60'"
            >
              <td>
                <!-- 表格用 min-width: max-content 撑开，truncate 需要显式宽度上限才生效 -->
                <div class="w-60 max-w-[15rem]">
                  <div class="flex items-center gap-2">
                    <span
                      class="truncate font-medium text-gray-900 dark:text-white"
                      :title="channel.name"
                    >
                      {{ channel.name }}
                    </span>
                    <Badge v-if="channel.excluded" tone="gray">已排除</Badge>
                    <Badge
                      v-else-if="!channel.managed"
                      tone="gray"
                      :title="unmanagedReason(channel)"
                    >
                      未受管
                    </Badge>
                  </div>
                  <p class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">
                    #{{ channel.id }} · {{ channel.platform }} · {{ channel.type }}
                  </p>
                  <p
                    class="mt-0.5 truncate text-xs text-gray-400 dark:text-dark-500"
                    :title="groupNames(channel)"
                  >
                    {{ groupNames(channel) }}
                  </p>
                </div>
              </td>

              <td>
                <ScoreRing
                  :score="channel.health_score"
                  :short="channel.short_score"
                  :long="channel.long_score"
                  :sample-count="channel.sample_count"
                  show-detail
                />
              </td>

              <td>
                <SampleStrip :samples="channel.recent" />
                <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
                  {{ channel.sample_count }} 条样本 · {{ formatRelative(channel.last_sample_at) }}
                </p>
              </td>

              <td>
                <p class="text-sm text-gray-900 dark:text-white">
                  P95 {{ formatMs(channel.ttfb_p95_ms) }}
                </p>
                <p class="text-xs text-gray-500 dark:text-dark-400">
                  P50 {{ formatMs(channel.ttfb_p50_ms) }}
                </p>
              </td>

              <td>
                <Badge :tone="multiplierTone(channel.multiplier)">
                  {{ formatMultiplier(channel.multiplier) }}
                </Badge>
                <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
                  {{ multiplierSourceLabel(channel) }}
                </p>
              </td>

              <td>
                <p class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ channel.weight.toFixed(0) }}
                </p>
              </td>

              <td>
                <p class="text-sm text-gray-900 dark:text-white">
                  {{ channel.priority }}
                  <span
                    v-if="channel.desired_priority && channel.desired_priority !== channel.priority"
                    class="text-primary-600 dark:text-primary-400"
                  >
                    → {{ channel.desired_priority }}
                  </span>
                </p>
                <p class="text-xs text-gray-500 dark:text-dark-400">
                  负载 {{ channel.load_factor ?? '—' }}
                  <span
                    v-if="
                      channel.desired_load_factor !== undefined &&
                      channel.desired_load_factor !== channel.load_factor
                    "
                    class="text-primary-600 dark:text-primary-400"
                  >
                    → {{ channel.desired_load_factor }}
                  </span>
                  · 并发 {{ channel.concurrency }}
                </p>
              </td>

              <td>
                <!-- 徽章不换行：「保底强留」这类四字标签在窄列里会被折断 -->
                <Badge
                  :tone="healthMeta(channel.health).tone"
                  dot
                  class="whitespace-nowrap"
                  :title="
                    channel.apply_pending && channel.desired_health
                      ? `引擎期望：${healthMeta(channel.desired_health).label}。${channel.apply_error || '尚未写回 sub2api'}。`
                      : undefined
                  "
                >
                  {{ healthMeta(channel.health).label }}
                </Badge>
                <p
                  v-if="channel.fused_reason || channel.last_error"
                  class="mt-1 max-w-[12rem] truncate text-xs text-gray-500 dark:text-dark-400"
                  :title="channel.fused_reason || channel.last_error"
                >
                  {{ channel.fused_reason || channel.last_error }}
                </p>
                <p
                  v-if="!channel.schedulable && !channel.paused && !channel.excluded"
                  class="mt-1 text-xs text-red-500"
                >
                  已停止调度
                </p>
                <!--
                  上游还有三个「到点自愈」的窗口会让渠道接不到流量：限流、临时不可调度、
                  过载退避。它们不体现在 schedulable 上，Guardian 也探测不出来 ——
                  不显示的话，页面会出现「健康分 100 却没有流量」而无从解释。
                -->
                <p
                  v-if="channel.upstream_block && channel.upstream_block !== 'unschedulable' && channel.upstream_block !== 'disabled'"
                  class="mt-1 max-w-[12rem] truncate text-xs text-primary-600 dark:text-primary-400"
                  :title="`${channel.upstream_block_text}。这是 sub2api 自己记录的窗口，到点自动恢复，Guardian 不会插手。`"
                >
                  {{ channel.upstream_block_text }}
                </p>
                <p
                  v-if="channel.model_rewritten"
                  class="mt-1 flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400"
                  :title="`指定 ${channel.last_request_model}，sub2api 实际用了 ${channel.last_probe_model}。这是该账号在 sub2api 侧的模型映射所致，需到 sub2api 后台调整该账号的模型映射。`"
                >
                  <Icon name="exclamationTriangle" size="xs" />
                  模型被上游改写
                </p>
              </td>

              <td>
                <!--
                  固定两列网格：按钮数量随渠道类型变化，flex-wrap 会让每行的
                  按钮数随宽度漂移，行高忽高忽低。网格保证始终「一行两个」。

                  宽度按「两个图标按钮 + 间距」给足，同时容得下跨两列的
                  「取消排除」四字标签，避免它被折行。
                -->
                <div class="ml-auto grid w-[6.5rem] grid-cols-2 justify-items-stretch gap-1.5">
                  <!-- 已排除的渠道只提供「取消排除」，其余操作对它没有意义 -->
                  <button
                    v-if="channel.excluded"
                    type="button"
                    class="btn btn-success btn-sm col-span-2 gap-1 whitespace-nowrap px-2"
                    :disabled="guardian.busy"
                    title="取消排除，重新纳入调度"
                    @click="setExcluded(channel, false)"
                  >
                    <Icon name="play" size="sm" />
                    取消排除
                  </button>
                  <template v-else>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="guardian.busy"
                    title="立即探测"
                    @click="probe(channel)"
                  >
                    <Icon name="beaker" size="sm" />
                  </button>
                  <button
                    v-if="!channel.paused"
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="guardian.busy || channel.excluded"
                    title="暂停调度（不会被健康分回升自动恢复）"
                    @click="setPaused(channel, true)"
                  >
                    <Icon name="pause" size="sm" />
                  </button>
                  <button
                    v-else
                    type="button"
                    class="btn btn-success btn-sm"
                    :disabled="guardian.busy"
                    title="恢复调度"
                    @click="setPaused(channel, false)"
                  >
                    <Icon name="play" size="sm" />
                  </button>
                  <button
                    v-if="channel.health !== 'fused'"
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="guardian.busy || channel.paused || channel.excluded"
                    title="手动熔断"
                    @click="fuse(channel)"
                  >
                    <Icon name="ban" size="sm" />
                  </button>
                  <button
                    v-else
                    type="button"
                    class="btn btn-success btn-sm"
                    :disabled="guardian.busy"
                    title="解除熔断"
                    @click="recover(channel)"
                  >
                    <Icon name="refresh" size="sm" />
                  </button>
                  </template>
                  <button
                    v-if="isAPIKeyType(channel.type)"
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="guardian.busy || isSyncingMultiplier(channel.id)"
                    title="同步上游倍率"
                    @click="syncUpstreamMultiplier(channel)"
                  >
                    <Icon
                      name="sync"
                      size="sm"
                      :class="isSyncingMultiplier(channel.id) ? 'animate-spin' : ''"
                    />
                  </button>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :class="[
                      channel.excluded && !isAPIKeyType(channel.type) && 'col-span-2',
                      !channel.excluded && isAPIKeyType(channel.type) && 'col-span-2'
                    ]"
                    title="编辑"
                    @click="openEditor(channel)"
                  >
                    <Icon name="edit" size="sm" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>

        <EmptyState
          v-if="!filtered.length"
          icon="server"
          title="没有匹配的渠道"
          description="调整筛选条件，或先同步 sub2api 的分组与账号。"
        />
      </div>

      <div v-if="filtered.length" class="card-footer">
        <MiniPager
          :page="page"
          :page-size="pageSize"
          :total="filtered.length"
          @update:page="page = $event"
        />
      </div>
    </div>

    <Modal
      :open="editor.open"
      :title="`渠道设置 · ${editor.name}`"
      subtitle="优先级、负载因子与并发会写回 sub2api；调度倍率只保存在 Guardian 内部。"
      @close="editor.open = false"
    >
      <div class="space-y-4">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            v-model="editor.priority"
            label="优先级"
            type="number"
            :min="1"
            hint="数值越小越优先"
          />
          <Field v-model="editor.loadFactor" label="负载因子" type="number" :min="1" />
          <Field v-model="editor.concurrency" label="并发上限" type="number" :min="1" />
        </div>

        <div
          class="rounded-xl border border-primary-200 bg-primary-50/60 p-4 dark:border-primary-900/50 dark:bg-primary-900/10"
        >
          <Field
            v-model="editor.multiplier"
            label="调度倍率"
            type="number"
            :min="0"
            :step="0.01"
            :disabled="editor.upstreamMultiplierEnabled && !editor.multiplierLinked"
            :hint="
              editor.multiplierLinked
                ? '当前值由渠道管理按 URL + API Key 同步，修改后解除本次关联'
                : editor.upstreamMultiplierEnabled
                ? '实时倍率开启时人工值暂不生效，关闭后自动恢复'
                : '越低越优先。仅供调度系统使用，不会写回 sub2api；填 0 表示回落到类型默认值'
            "
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
            类型默认：账号类型渠道 {{ DEFAULT_OAUTH_MULTIPLIER }}（优先使用）· API Key
            {{ DEFAULT_APIKEY_MULTIPLIER }}
          </p>
          <div
            v-if="isAPIKeyType(editor.type)"
            class="mt-4 border-t border-primary-200 pt-4 dark:border-primary-900/50"
          >
            <SwitchRow
              v-model="editor.upstreamMultiplierEnabled"
              label="实时使用上游倍率"
              description="自动定期读取 API Key 上游；失败时保留最近成功倍率，尚无成功记录时沿用 Sub2API 账号值"
            />
            <div class="mt-3 space-y-3">
              <SwitchRow
                v-model="editor.upstreamMultiplierBreakerEnabled"
                :disabled="!editor.upstreamMultiplierEnabled"
                label="倍率超阈值直接熔断"
                description="最近成功读取的上游倍率超过设置上限时停止调度；仍遵守分组保底规则"
              />
              <Field
                v-model="editor.upstreamMultiplierThreshold"
                label="上游倍率上限"
                type="number"
                :min="0.01"
                :step="0.01"
                :disabled="!editor.upstreamMultiplierEnabled"
                hint="只有开启实时倍率后才生效；等于阈值不会熔断"
              />
            </div>
            <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
              最近成功同步：
              <span class="font-medium text-gray-700 dark:text-dark-200">
                {{
                  editor.upstreamMultiplier > 0
                    ? `${formatMultiplier(editor.upstreamMultiplier)} · ${formatRelative(editor.upstreamMultiplierUpdatedAt)}`
                    : '暂无'
                }}
              </span>
              · Sub2API 账号值 {{ formatMultiplier(editor.upstreamRateMultiplier) }}
            </p>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <SwitchRow
            v-model="editor.paused"
            label="暂停调度"
            description="不接流量但继续监控计分，不会被健康分回升自动恢复"
          />
          <SwitchRow
            v-model="editor.excluded"
            label="排除该渠道"
            description="不探测、不调度、不计分，并恢复原始配置"
          />
        </div>

        <div>
          <p class="input-label">测活模型</p>
          <div class="flex gap-2">
            <input v-model="editor.testModel" class="input" placeholder="留空则用全局默认模型" />
            <button
              type="button"
              class="btn btn-secondary whitespace-nowrap"
              :disabled="modelsLoading"
              @click="loadModels"
            >
              <Icon name="sync" size="sm" />
              拉取模型
            </button>
          </div>

          <div
            v-if="editor.modelRewritten"
            class="mt-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs dark:border-amber-800 dark:bg-amber-900/20"
          >
            <div class="flex items-start gap-2 text-amber-700 dark:text-amber-300">
              <Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" />
              <div class="space-y-1">
                <p class="font-medium">这里指定的模型被 sub2api 改写了</p>
                <p>
                  最近一次探测请求
                  <code class="code">{{ editor.lastRequestModel || '（未指定）' }}</code>
                  ，sub2api 实际使用了
                  <code class="code">{{ editor.lastProbeModel }}</code>
                  。
                </p>
                <p>
                  原因是该账号在 sub2api 侧配置了模型映射（含通配符），网关会在测试时
                  把请求的模型重写掉。Guardian 无法绕过它 ——
                  请到 sub2api 后台调整该账号的「模型映射」。
                </p>
              </div>
            </div>
          </div>
          <div v-if="models.length" class="mt-2 flex max-h-32 flex-wrap gap-1.5 overflow-y-auto">
            <button
              v-for="model in models"
              :key="model"
              type="button"
              class="badge badge-gray hover:badge-primary"
              @click="editor.testModel = model"
            >
              {{ model }}
            </button>
          </div>
        </div>
      </div>

      <template #footer>
        <button type="button" class="btn btn-secondary" @click="editor.open = false">取消</button>
        <button type="button" class="btn btn-primary" :disabled="guardian.busy" @click="saveChannel">
          保存
        </button>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import Toggle from '@/components/Toggle.vue'
import Modal from '@/components/Modal.vue'
import Field from '@/components/Field.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import EmptyState from '@/components/EmptyState.vue'
import ScoreRing from '@/components/ScoreRing.vue'
import SampleStrip from '@/components/SampleStrip.vue'
import MiniPager from '@/components/MiniPager.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import {
  formatMs,
  formatMultiplier,
  formatRelative,
  healthMeta,
  multiplierTone
} from '@/lib/format'
import type { Channel } from '@/lib/types'

/** 与后端 policy.DefaultOAuthMultiplier / DefaultAPIKeyMultiplier 对应，仅用于提示文案。 */
const DEFAULT_OAUTH_MULTIPLIER = 0.01
const DEFAULT_APIKEY_MULTIPLIER = 1

function isAPIKeyType(accountType: string): boolean {
  return ['apikey', 'api_key', 'key'].includes(accountType.trim().toLowerCase())
}

function multiplierSourceLabel(channel: Channel): string {
  switch (channel.multiplier_source) {
    case 'linked':
      return '渠道管理同步'
    case 'upstream':
      return '上游倍率'
    case 'upstream_fallback':
      return '等待同步，使用原值'
    case 'manual':
      return '人工设置'
    default:
      return '类型默认'
  }
}

const guardian = useGuardianStore()
const ui = useUIStore()

const search = ref('')
const groupFilter = ref('')
const healthFilter = ref('')
const typeFilter = ref('')
const managedOnly = ref(true)
const showExcluded = ref(false)

const page = ref(1)
const pageSize = 20

const models = ref<string[]>([])
const modelsLoading = ref(false)
const syncing = ref(false)
const syncingMultiplierIDs = ref<Set<number>>(new Set())

/** 只同步 sub2api 目录字段；后端不会探测，已有样本与评分保持不变。 */
async function syncNow() {
  syncing.value = true
  try {
    const result = await guardian.run(() => api.sync())
    const count = result.channels ?? guardian.channels.length
    ui.notify('success', `已从 sub2api 同步 ${count} 个渠道，测试数据未变更`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    syncing.value = false
  }
}

const channelTypes = computed(() => {
  const types = new Set<string>()
  for (const channel of guardian.channels) {
    if (channel.type) types.add(channel.type)
  }
  return Array.from(types).sort()
})

/** 状态计数按「受管 + 是否显示排除」的口径统计，与表格保持一致。 */
const scoped = computed(() =>
  guardian.channels.filter(channel => {
    if (managedOnly.value && !channel.managed) return false
    if (!showExcluded.value && channel.excluded) return false
    return true
  })
)

/**
 * 页签计数的口径刻意不含「显示已排除」开关。
 *
 * 否则「已排除」页签在开关关闭时永远显示 0 —— 而一个计数为 0 的页签
 * 看起来就是「没有这类渠道」，用户不会去点它，也就永远找不到自己排除过的渠道。
 */
const countScope = computed(() =>
  guardian.channels.filter(channel => !managedOnly.value || channel.managed)
)

const statusTabs = computed(() => {
  const count = (health: string) => countScope.value.filter(ch => ch.health === health).length
  return [
    { value: '', label: '全部', count: scoped.value.length },
    { value: 'healthy', label: '健康', count: count('healthy') },
    { value: 'degraded', label: '降级', count: count('degraded') },
    { value: 'fused', label: '已熔断', count: count('fused') },
    { value: 'survivor', label: '保底强留', count: count('survivor') },
    { value: 'paused', label: '已暂停', count: count('paused') },
    { value: 'excluded', label: '已排除', count: count('excluded') },
    { value: 'unknown', label: '待探测', count: count('unknown') }
  ]
})

const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return scoped.value.filter(channel => {
    if (healthFilter.value && channel.health !== healthFilter.value) return false
    if (typeFilter.value && channel.type !== typeFilter.value) return false
    if (groupFilter.value && !(channel.groups ?? []).some(g => String(g.id) === groupFilter.value)) {
      return false
    }
    if (!needle) return true
    const haystack = [
      channel.name,
      channel.platform,
      channel.type,
      channel.test_model,
      String(channel.id),
      ...(channel.groups ?? []).map(g => g.name)
    ]
      .join(' ')
      .toLowerCase()
    return haystack.includes(needle)
  })
})

const paged = computed(() => {
  const start = (page.value - 1) * pageSize
  return filtered.value.slice(start, start + pageSize)
})

/** 筛选结果变化时收敛页码，避免停在越界的空白页。 */
watch(
  () => filtered.value.length,
  total => {
    const totalPages = Math.max(1, Math.ceil(total / pageSize))
    if (page.value > totalPages) page.value = totalPages
  }
)

function setHealthFilter(value: string) {
  healthFilter.value = value
  // 排除态只能通过「显示已排除」看到，点这个页签时顺手打开它。
  if (value === 'excluded') showExcluded.value = true
  page.value = 1
}

watch([search, groupFilter, typeFilter, managedOnly, showExcluded], () => {
  page.value = 1
})

// 关掉「显示已排除」时若正停在「已排除」页签，列表会空白。退回「全部」。
watch(showExcluded, visible => {
  if (!visible && healthFilter.value === 'excluded') {
    healthFilter.value = ''
  }
})

const editor = reactive({
  open: false,
  id: 0,
  name: '',
  priority: 1,
  loadFactor: 1,
  concurrency: 1,
  multiplier: 0,
  type: '',
  multiplierLinked: false,
  upstreamMultiplierEnabled: false,
  upstreamMultiplierBreakerEnabled: false,
  upstreamMultiplierThreshold: 0,
  upstreamRateMultiplier: 0,
  upstreamMultiplier: 0,
  upstreamMultiplierUpdatedAt: '',
  paused: false,
  excluded: false,
  testModel: '',

  // 模型偏差的只读信息，用于在弹窗里解释「为什么指定的模型没生效」。
  modelRewritten: false,
  lastRequestModel: '',
  lastProbeModel: '',

  // 打开弹窗时的原值，保存时用来算出真正改了什么。
  originalPriority: 1,
  originalLoadFactor: 1,
  originalConcurrency: 1,
  originalMultiplier: 0,
  originalUpstreamMultiplierEnabled: false,
  originalUpstreamMultiplierBreakerEnabled: false,
  originalUpstreamMultiplierThreshold: 0,
  originalTestModel: '',
  originalExcluded: false,
  originalPaused: false
})

function openEditor(channel: Channel) {
  models.value = channel.models ?? []
  editor.open = true
  editor.id = channel.id
  editor.name = channel.name
  editor.priority = channel.priority
  editor.loadFactor = channel.load_factor ?? 1
  editor.concurrency = channel.concurrency
  editor.type = channel.type
  // 联动或实时倍率开启时生效值可能来自自动来源，人工配置通过独立字段回显并保留。
  editor.multiplier =
    channel.linked_multiplier ??
    channel.manual_multiplier ??
    (channel.multiplier_source === 'manual' ? channel.multiplier : 0)
  editor.multiplierLinked = channel.multiplier_linked
  editor.upstreamMultiplierEnabled = channel.upstream_multiplier_enabled
  editor.upstreamMultiplierBreakerEnabled = channel.upstream_multiplier_breaker_enabled
  editor.upstreamMultiplierThreshold = channel.upstream_multiplier_threshold ?? 0
  editor.upstreamRateMultiplier = channel.rate_multiplier
  editor.upstreamMultiplier = channel.upstream_multiplier ?? 0
  editor.upstreamMultiplierUpdatedAt = channel.upstream_multiplier_updated_at ?? ''
  editor.paused = channel.paused
  editor.excluded = channel.excluded
  editor.testModel = channel.test_model ?? ''
  editor.modelRewritten = channel.model_rewritten
  editor.lastRequestModel = channel.last_request_model ?? ''
  editor.lastProbeModel = channel.last_probe_model ?? ''

  editor.originalPriority = editor.priority
  editor.originalLoadFactor = editor.loadFactor
  editor.originalConcurrency = editor.concurrency
  editor.originalMultiplier = editor.multiplier
  editor.originalUpstreamMultiplierEnabled = editor.upstreamMultiplierEnabled
  editor.originalUpstreamMultiplierBreakerEnabled = editor.upstreamMultiplierBreakerEnabled
  editor.originalUpstreamMultiplierThreshold = editor.upstreamMultiplierThreshold
  editor.originalTestModel = editor.testModel
  editor.originalExcluded = editor.excluded
  editor.originalPaused = editor.paused
}

async function loadModels() {
  modelsLoading.value = true
  try {
    const data = await api.channelModels(editor.id)
    models.value = data.models
    ui.notify('success', `已拉取 ${data.models.length} 个模型`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    modelsLoading.value = false
  }
}

// saveChannel 只提交真正变化的部分：每个请求都会打到上游，
// 全量提交会让一次保存变成好几轮往返。
async function saveChannel() {
  if (
    editor.upstreamMultiplierEnabled &&
    editor.upstreamMultiplierBreakerEnabled &&
    editor.upstreamMultiplierThreshold <= 0
  ) {
    ui.notify('error', '开启倍率阈值熔断时必须设置大于 0 的上游倍率上限')
    return
  }

  const changed: Record<string, unknown> = {}
  if (editor.priority !== editor.originalPriority) changed.priority = editor.priority
  if (editor.loadFactor !== editor.originalLoadFactor) changed.load_factor = editor.loadFactor
  if (editor.concurrency !== editor.originalConcurrency) changed.concurrency = editor.concurrency
  if (editor.multiplier !== editor.originalMultiplier) changed.multiplier = editor.multiplier
  if (editor.upstreamMultiplierEnabled !== editor.originalUpstreamMultiplierEnabled) {
    changed.upstream_multiplier_enabled = editor.upstreamMultiplierEnabled
  }
  if (editor.upstreamMultiplierEnabled) {
    if (
      editor.upstreamMultiplierBreakerEnabled !==
      editor.originalUpstreamMultiplierBreakerEnabled
    ) {
      changed.upstream_multiplier_breaker_enabled = editor.upstreamMultiplierBreakerEnabled
    }
    if (editor.upstreamMultiplierThreshold !== editor.originalUpstreamMultiplierThreshold) {
      changed.upstream_multiplier_threshold = editor.upstreamMultiplierThreshold
    }
  }

  const nothingChanged =
    Object.keys(changed).length === 0 &&
    editor.testModel === editor.originalTestModel &&
    editor.excluded === editor.originalExcluded &&
    editor.paused === editor.originalPaused

  if (nothingChanged) {
    editor.open = false
    return
  }

  try {
    await guardian.run(async () => {
      if (editor.excluded !== editor.originalExcluded) {
        await api.excludeChannel(editor.id, editor.excluded)
      }
      if (editor.testModel !== editor.originalTestModel) {
        await api.setChannelTestModel(editor.id, editor.testModel)
      }
      if (Object.keys(changed).length > 0) {
        await api.updateChannel(editor.id, changed)
      }
      // 暂停走专用端点：它要落进策略名单才能跨轮次持续生效，
      // 放在最后执行，避免被上面的普通更新覆盖掉 schedulable。
      if (editor.paused !== editor.originalPaused) {
        await api.pauseChannel(editor.id, editor.paused)
      }
    })
    await guardian.loadPolicy()
    editor.open = false
    ui.notify('success', `渠道「${editor.name}」已保存`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function probe(channel: Channel) {
  ui.notify('info', `正在探测「${channel.name}」…`)
  try {
    await guardian.run(() => api.probeChannel(channel.id))
    ui.notify('success', `「${channel.name}」探测完成`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

function isSyncingMultiplier(accountID: number): boolean {
  return syncingMultiplierIDs.value.has(accountID)
}

async function syncUpstreamMultiplier(channel: Channel) {
  syncingMultiplierIDs.value = new Set(syncingMultiplierIDs.value).add(channel.id)
  try {
    const result = await api.syncUpstreamMultiplier(channel.id)
    await guardian.refresh({ silent: true })
    const changed = Math.abs(result.multiplier - result.previous_multiplier) > 1e-9
    ui.notify(
      'success',
      changed
        ? `「${channel.name}」上游倍率已更新：${formatMultiplier(result.previous_multiplier)} → ${formatMultiplier(result.multiplier)}`
        : `「${channel.name}」上游倍率已同步：${formatMultiplier(result.multiplier)}`
    )
  } catch (err) {
    const detail = (err as Error).message.replace(/^同步上游倍率失败，继续使用原倍率：\s*/, '')
    ui.notify(
      'error',
      `「${channel.name}」同步上游倍率失败，继续使用原倍率 ${formatMultiplier(channel.multiplier)}：${detail}`
    )
  } finally {
    const next = new Set(syncingMultiplierIDs.value)
    next.delete(channel.id)
    syncingMultiplierIDs.value = next
  }
}

async function fuse(channel: Channel) {
  if (!window.confirm(`确定要手动熔断「${channel.name}」吗？该渠道将停止调度。`)) return
  try {
    await guardian.run(() => api.fuseChannel(channel.id, '人工熔断'))
    ui.notify('success', `「${channel.name}」已熔断`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function recover(channel: Channel) {
  try {
    await guardian.run(() => api.recoverChannel(channel.id))
    ui.notify('success', `「${channel.name}」已解除熔断`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function setPaused(channel: Channel, paused: boolean) {
  if (
    paused &&
    !window.confirm(`确定要暂停「${channel.name}」吗？该渠道将不再接收流量，直到你手动恢复。`)
  ) {
    return
  }
  try {
    await guardian.run(() => api.pauseChannel(channel.id, paused))
    await guardian.loadPolicy()
    ui.notify('success', `「${channel.name}」${paused ? '已暂停调度' : '已恢复调度'}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

// unmanagedReason 说明渠道为什么不受管。
//
// 「所属分组全部被排除」的情况不在此列 —— 那类渠道后端压根不会返回。
function unmanagedReason(channel: Channel): string {
  if (!(channel.groups ?? []).length) {
    return '该渠道未归属任何分组'
  }
  const policy = guardian.policy
  if (policy?.managed_account_types.length && !policy.managed_account_types.includes(channel.type)) {
    return `账号类型 ${channel.type} 不在受管类型名单内`
  }
  if (policy?.managed_platforms.length && !policy.managed_platforms.includes(channel.platform)) {
    return `平台 ${channel.platform} 不在受管平台名单内`
  }
  return '所属分组未参与守护（可在策略配置 → 守护范围里勾选）'
}

async function setExcluded(channel: Channel, excluded: boolean) {
  try {
    await guardian.run(() => api.excludeChannel(channel.id, excluded))
    await guardian.loadPolicy()
    ui.notify('success', `「${channel.name}」${excluded ? '已排除' : '已取消排除'}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

function groupNames(channel: Channel): string {
  return (channel.groups ?? []).map(group => group.name).join(' / ') || '未分组'
}
</script>
