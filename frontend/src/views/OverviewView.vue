<template>
  <AppLayout title="总览" subtitle="24 小时自动化守护：保证每个分组至少有一个渠道存活">
    <section class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <StatCard
        v-for="tile in tiles"
        :key="tile.key"
        :label="tile.label"
        :value="tile.value"
        :meta="tile.meta"
        :tone="tile.tone"
        :icon="iconFor(tile.key)"
        :decimals="tile.key === 'score' ? 1 : 0"
      />
    </section>

    <section class="card">
      <div class="card-header flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">分组健康矩阵</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            进度条是「当前可调度存活账号」的占比；点击进入分组调度。
          </p>
        </div>
        <div class="flex items-center gap-2">
          <!--
            立即同步不做探测，只重新拉取 sub2api 的渠道清单并重算聚合。
            限流窗口、关闭调用这些都是上游字段，拉一次就能对上，
            不必等下一轮探测（默认 300 秒）。
          -->
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="syncing || guardian.busy"
            title="立即从 sub2api 重新拉取渠道状态并重算每个分组的存活数，不触发探测"
            @click="syncNow"
          >
            <Icon name="sync" size="sm" :class="syncing ? 'animate-spin' : ''" />
            {{ syncing ? '同步中…' : '立即同步' }}
          </button>
          <RouterLink to="/groups" class="btn btn-secondary btn-sm">
            分组调度
            <Icon name="arrowRight" size="sm" />
          </RouterLink>
        </div>
      </div>

      <div class="p-6">
        <EmptyState
          v-if="!managedGroups.length"
          title="还没有参与守护的分组"
          description="先在连接设置里配置 sub2api，再到策略配置里选择参与守护的分组。"
        />
        <div v-else class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <RouterLink
            v-for="group in managedGroups"
            :key="group.id"
            :to="`/groups?focus=${group.id}`"
            class="card card-hover block p-4"
          >
            <div class="flex items-start justify-between gap-2">
              <div class="min-w-0">
                <p class="truncate font-medium text-gray-900 dark:text-white">{{ group.name }}</p>
                <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                  #{{ group.id }} · {{ group.platform || '全平台' }} · 倍率 {{ group.rate_multiplier }}
                </p>
              </div>
              <Badge :tone="groupStatusMeta(group.state.status).tone" dot>
                {{ groupStatusMeta(group.state.status).label }}
              </Badge>
            </div>

            <div class="mt-3 flex items-center justify-between gap-3">
              <Badge tone="primary">{{ strategyLabel(group.strategy) }}</Badge>
              <div class="text-right">
                <p class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ group.state.available_accounts }}/{{ group.state.total_accounts }}
                </p>
                <p class="text-[11px] text-gray-500 dark:text-dark-400">存活 / 总数</p>
              </div>
            </div>

            <!-- 存活数与引擎的可用池、熔断保底使用同一套口径。 -->
            <div class="mt-3 flex items-center gap-3">
              <div
                class="progress flex-1"
                :title="`存活 ${group.state.available_accounts} · 限流 ${group.state.rate_limited_accounts}（自愈） · 需处理 ${attentionCount(group)} · 不可用 ${group.state.excluded_accounts} · 待探测 ${group.state.pending_accounts} · 共 ${group.state.total_accounts}`"
              >
                <div
                  class="h-full rounded-full transition-all duration-300"
                  :class="ratioTone(group)"
                  :style="{ width: `${aliveRatio(group)}%` }"
                />
              </div>
              <span class="whitespace-nowrap text-xs text-gray-500 dark:text-dark-400">
                {{ aliveRatio(group) }}% · 均分 {{ group.state.avg_score.toFixed(0) }}
              </span>
            </div>

            <!--
              分开显示「限流」与「需要处理」两类不正常。
              限流会随窗口重置自愈、渠道仍在池子里；剩下的降级与熔断才要人介入。
              只显示一个「部分异常」的话，运维分不清该不该动手。
            -->
            <p
              v-if="attentionCount(group) > 0"
              class="mt-2 text-xs text-amber-600 dark:text-amber-400"
            >
              {{ attentionCount(group) }} 个渠道需要处理<template
                v-if="group.state.rate_limited_accounts > 0"
              >
                · {{ group.state.rate_limited_accounts }} 个限流中（会自愈）</template
              >
            </p>
            <p
              v-else-if="group.state.rate_limited_accounts > 0"
              class="mt-2 text-xs text-primary-600 dark:text-primary-400"
            >
              {{ group.state.rate_limited_accounts }} 个渠道限流中，等窗口重置会自动恢复
            </p>

            <p v-if="group.state.survivor_account_id" class="mt-2 text-xs text-amber-600 dark:text-amber-400">
              保底强留：渠道 #{{ group.state.survivor_account_id }}
            </p>
            <p v-else-if="group.state.pending_accounts > 0" class="mt-2 text-xs text-gray-400 dark:text-dark-500">
              {{ group.state.pending_accounts }} 个渠道待首次探测
            </p>
          </RouterLink>
        </div>
      </div>
    </section>

    <section class="grid grid-cols-1 gap-6 xl:grid-cols-5">
      <div class="card xl:col-span-3">
        <div class="card-header flex items-center justify-between">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">需要关注的渠道</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">熔断、保底与降级中的渠道优先展示。</p>
          </div>
          <RouterLink to="/channels" class="btn btn-secondary btn-sm">全部渠道</RouterLink>
        </div>
        <div class="p-4">
          <EmptyState
            v-if="!attention.length"
            icon="shield"
            title="所有受管渠道都健康"
            description="没有熔断、保底或降级中的渠道。"
          />
          <div v-else class="space-y-2">
            <div
              v-for="channel in attention"
              :key="channel.id"
              class="flex items-center gap-3 rounded-xl border border-gray-100 p-3 dark:border-dark-700"
            >
              <ScoreRing
                :score="channel.health_score"
                :sample-count="channel.sample_count"
              />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <p class="truncate font-medium text-gray-900 dark:text-white">{{ channel.name }}</p>
                  <Badge :tone="healthMeta(channel.health).tone">{{ healthMeta(channel.health).label }}</Badge>
                </div>
                <p class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">
                  {{ channel.fused_reason || channel.last_error || '等待下一轮评估' }}
                </p>
              </div>
              <SampleStrip :samples="channel.recent" class="hidden sm:flex" />
            </div>
          </div>
        </div>
      </div>

      <div class="card xl:col-span-2">
        <div class="card-header flex items-center justify-between">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">最近事件</h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">熔断、回池与写回记录。</p>
          </div>
          <RouterLink to="/events" class="btn btn-secondary btn-sm">全部</RouterLink>
        </div>
        <div class="p-4">
          <EmptyState v-if="!events.length" icon="document" title="暂无事件" />
          <ul v-else class="space-y-3">
            <li v-for="event in events" :key="event.id" class="flex gap-3">
              <span class="mt-1.5 h-2 w-2 flex-shrink-0 rounded-full" :class="dotFor(event.level)" />
              <div class="min-w-0">
                <p class="truncate text-sm text-gray-800 dark:text-gray-200">{{ event.message }}</p>
                <p class="mt-0.5 text-xs text-gray-400 dark:text-dark-500">
                  {{ event.action }} · {{ formatRelative(event.created_at) }}
                </p>
              </div>
            </li>
          </ul>
        </div>
      </div>
    </section>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import StatCard from '@/components/StatCard.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import ScoreRing from '@/components/ScoreRing.vue'
import SampleStrip from '@/components/SampleStrip.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import { formatRelative, groupStatusMeta, healthMeta, strategyLabel } from '@/lib/format'
import type { IconName } from '@/lib/icons'
import type { Group } from '@/lib/types'

const guardian = useGuardianStore()
const ui = useUIStore()
const syncing = ref(false)

/**
 * syncNow 立即从 sub2api 重新拉取渠道清单并重算每个分组的聚合。
 *
 * 与「跑一轮」的区别：这里不发探测请求，只读上游的账号列表。
 * 矩阵里的存活数取决于上游字段（是否关闭调用、限流窗口有没有过），
 * 拉一次就能与网站对上，不需要等下一轮探测。
 */
async function syncNow() {
  syncing.value = true
  try {
    const result = await guardian.run(() => api.sync())
    const parts: string[] = []
    if (result.groups != null) parts.push(`${result.groups} 个分组`)
    if (result.available_accounts != null && result.total_accounts != null) {
      parts.push(`存活 ${result.available_accounts}/${result.total_accounts}`)
    }
    if (result.rate_limited_accounts) parts.push(`限流 ${result.rate_limited_accounts}`)
    ui.notify('success', parts.length ? `已同步：${parts.join(' · ')}` : '已从 sub2api 重新同步')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    syncing.value = false
  }
}

const tiles = computed(() => guardian.overview?.tiles ?? [])
const events = computed(() => guardian.overview?.events?.slice(0, 8) ?? [])
const managedGroups = computed(() => guardian.groups.filter(group => group.managed))

const attention = computed(() =>
  guardian.channels
    .filter(channel => ['fused', 'survivor', 'degraded'].includes(channel.health))
    .slice(0, 8)
)

/**
 * aliveRatio 与引擎的可用池口径一致：账号必须当前可被 sub2api 调度，
 * 有健康样本时分数还必须达到策略配置的计入可用池最低分。
 * 熔断、暂停、排除、上游阻塞以及低于阈值的账号都不计入。
 */
function aliveRatio(group: Group): number {
  if (!group.state.total_accounts) return 0
  return Math.round((group.state.available_accounts / group.state.total_accounts) * 100)
}

/** ratioTone 让进度条颜色跟着存活占比走，低占比一眼能看出来。 */
function ratioTone(group: Group): string {
  const ratio = aliveRatio(group)
  if (ratio >= 80) return 'bg-emerald-500'
  if (ratio >= 50) return 'bg-amber-500'
  return 'bg-red-500'
}

/**
 * attentionCount 是「真的需要人看一眼」的渠道数。
 *
 * 刻意把限流排除在外：限流会随窗口重置自愈、渠道仍留在池子里，
 * 混进来会让每次上游限流都看起来像一堆待处理故障，把真问题淹掉。
 */
function attentionCount(group: Group): number {
  const s = group.state
  const realDegraded = Math.max(0, s.degraded_accounts - s.rate_limited_accounts)
  return realDegraded + s.fused_accounts
}

function iconFor(key: string): IconName {
  return (
    {
      channels: 'server',
      score: 'shield',
      concurrency: 'bolt',
      risk: 'exclamationTriangle'
    }[key] ?? 'chartBar'
  ) as IconName
}

function dotFor(level: string): string {
  return {
    error: 'bg-red-500',
    warn: 'bg-amber-500',
    info: 'bg-primary-500'
  }[level] ?? 'bg-gray-400'
}
</script>
