<template>
  <AppLayout :title="isSummaryPage ? '渠道汇总' : '渠道列表'" :subtitle="isSummaryPage ? '集中查看渠道余额、令牌和当前倍率' : '在渠道目录中选择渠道并查看详细信息'">
    <div v-if="isSummaryPage" class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">渠道汇总</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">集中查看所有上游渠道的余额、令牌和当前倍率。</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button type="button" class="btn btn-secondary btn-sm" @click="openEmailEditor">
          <Icon name="mail" size="sm" />
          邮件设置
        </button>
        <button type="button" class="btn btn-secondary btn-sm" @click="openWeComEditor">
          <Icon name="chat" size="sm" />
          企微设置
        </button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || !syncableChannels.length" @click="syncAll">
          <Icon name="refresh" size="sm" />
          刷新全部
        </button>
        <button type="button" class="btn btn-primary btn-sm" @click="openChannelEditor()">
          <Icon name="plus" size="sm" />
          新增渠道
        </button>
      </div>
    </div>

    <template v-if="isSummaryPage">
      <div v-if="summaryError" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
        <Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" />
        <span class="min-w-0 flex-1">{{ summaryError }}</span>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="summaryLoading" @click="loadChannels">重试</button>
      </div>
      <div v-if="summaryLoading && !channels.length && !summaryError" class="card flex min-h-48 items-center justify-center">
        <div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><span class="spinner text-primary-500" />正在加载渠道汇总</div>
      </div>
      <div v-else-if="channels.length" class="space-y-6">
        <section v-for="group in summaryGroups" :key="group.key" class="space-y-3">
          <div class="flex items-center justify-between gap-3 border-b border-gray-200 pb-2 dark:border-dark-700">
            <h3 class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ group.label }}</h3>
            <Badge tone="gray">{{ group.items.length }}</Badge>
          </div>
          <div v-if="group.items.length" class="grid grid-cols-1 gap-3 xl:grid-cols-4 2xl:grid-cols-5">
            <article v-for="channel in group.items" :key="channel.id" class="card h-full overflow-hidden" :class="channel.ignored && 'bg-gray-50 dark:bg-dark-900/40'">
              <div class="p-4">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0">
                    <button type="button" class="max-w-full truncate text-left text-base font-semibold text-gray-900 hover:text-primary-600 dark:text-white dark:hover:text-primary-400" @click="selectChannel(channel)">
                      {{ channel.name }}
                    </button>
                    <div class="mt-1.5 flex flex-wrap gap-1.5">
                      <Badge :tone="channelTypeTone(channel.type)">{{ channelTypeLabel(channel.type) }}</Badge>
                      <Badge :tone="channelStatusMeta(channel).tone" dot>{{ channelStatusMeta(channel).label }}</Badge>
                    </div>
                  </div>
                  <div class="flex flex-shrink-0 items-center gap-1.5">
                    <div v-if="channel.recharge_methods?.length" class="flex items-center gap-0.5 text-gray-500 dark:text-dark-400">
                      <span v-for="method in channel.recharge_methods" :key="method" class="inline-flex h-6 w-6 items-center justify-center rounded-md bg-gray-100 dark:bg-dark-700" :title="rechargeMethodLabel(method)">
                        <RechargeMethodIcon :method="method" size="sm" />
                      </span>
                    </div>
                    <div class="flex gap-1">
                      <button v-if="channel.type !== 'other'" type="button" class="btn btn-ghost btn-icon" title="刷新渠道" :disabled="busy || channel.ignored" @click="syncChannel(channel)">
                        <Icon name="refresh" size="sm" :class="syncingID === channel.id && 'animate-spin'" />
                      </button>
                      <button type="button" class="btn btn-ghost btn-icon" :title="channel.ignored ? '取消忽略' : '忽略渠道'" :disabled="busy" @click="setIgnored(channel, !channel.ignored)">
                        <Icon :name="channel.ignored ? 'eye' : 'eyeOff'" size="sm" />
                      </button>
                    </div>
                  </div>
                </div>

                <div class="mt-3">
                  <p class="text-xs font-medium text-gray-500 dark:text-dark-400">当前余额</p>
                  <p class="mt-1 truncate text-xl font-semibold text-gray-900 dark:text-white">{{ summaryBalance(channel) }}</p>
                  <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">最近同步 {{ formatRelative(channel.last_sync_at) }}</p>
                  <div class="mt-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-gray-500 dark:text-dark-400">
                    <span>充值 {{ rechargeRatioLabel(channel) }}</span>
                    <span class="min-w-0 truncate" :title="channel.recharge_fee || '无'">手续费 {{ channel.recharge_fee || '无' }}</span>
                  </div>
                </div>

                <div class="mt-3 border-t border-gray-100 pt-2 dark:border-dark-700">
                  <div class="flex items-center justify-between text-xs font-medium text-gray-500 dark:text-dark-400"><span>令牌</span><span>当前倍率</span></div>
                  <div v-if="summaryTokenRows(channel).length" class="mt-1 divide-y divide-gray-100 dark:divide-dark-700">
                    <div v-for="(token, index) in summaryTokenRows(channel).slice(0, 4)" :key="rowKey(token, index)" class="flex min-h-7 items-center justify-between gap-3 py-0.5 text-sm">
                      <span class="min-w-0 truncate text-gray-700 dark:text-dark-200">{{ displayValue(token, ['name', 'title', 'id', 'ID'], `令牌 ${index + 1}`) }}</span>
                      <span class="inline-flex flex-shrink-0 items-center gap-0.5 font-medium text-gray-900 dark:text-white">
                        {{ tokenRatioLabel(channel, token) }}
                        <RatioChangeIndicator :change="tokenRatioChange(channel, token)" />
                      </span>
                    </div>
                  </div>
                  <p v-else class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ summaryErrors[channel.id] || (summaryLoading ? '正在加载…' : '暂无令牌缓存') }}</p>
                  <p v-if="summaryTokenRows(channel).length > 4" class="mt-1.5 text-xs text-gray-500 dark:text-dark-400">另有 {{ summaryTokenRows(channel).length - 4 }} 个令牌，请进入详情查看</p>
                </div>
              </div>
            </article>
          </div>
          <p v-else class="py-5 text-sm text-gray-500 dark:text-dark-400">暂无{{ group.label }}</p>
        </section>
      </div>
      <div v-else-if="!summaryError" class="card">
        <EmptyState icon="server" title="暂无渠道" description="新增渠道后可在这里统一查看余额、令牌和倍率。" />
      </div>
    </template>

    <template v-else>
      <div class="grid items-start gap-4 lg:grid-cols-[20rem_minmax(0,1fr)] xl:grid-cols-[22rem_minmax(0,1fr)]">
        <aside class="card min-w-0 overflow-hidden lg:sticky lg:top-24">
          <div class="border-b border-gray-100 p-4 dark:border-dark-700">
            <div class="flex items-center justify-between gap-2">
              <div class="min-w-0">
                <h2 class="truncate text-base font-semibold text-gray-900 dark:text-white">渠道目录</h2>
                <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ channels.length }} 个上游渠道</p>
              </div>
              <button type="button" class="btn btn-primary btn-icon" title="新增渠道" @click="openChannelEditor()"><Icon name="plus" size="sm" /></button>
            </div>
            <label class="relative mt-3 block">
              <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-2.5 text-gray-400" />
              <input v-model="search" class="input pl-9" placeholder="搜索名称、类型或地址" />
            </label>
          </div>
          <div class="max-h-[calc(100vh-15rem)] overflow-y-auto p-2">
            <div class="space-y-3">
              <details open>
                <summary class="flex cursor-pointer list-none items-center justify-between rounded-lg px-2 py-2 text-xs font-semibold text-gray-600 hover:bg-gray-50 dark:text-dark-300 dark:hover:bg-dark-800">
                  <span>使用中的渠道 <Badge tone="gray">{{ activeFiltered.length }}</Badge></span><Icon name="chevronDown" size="xs" />
                </summary>
                <div class="mt-1 space-y-1">
                  <button v-for="channel in activeFiltered" :key="channel.id" type="button" class="upstream-rail-item" :class="selectedID === channel.id && 'upstream-rail-item-selected'" @click="selectChannel(channel)">
                    <span class="h-full min-h-14 w-1 flex-shrink-0 rounded-full" :class="channelStatusClass(channel)" />
                    <span class="min-w-0 flex-1 text-left">
                      <span class="flex min-w-0 items-center gap-1.5"><strong class="truncate text-sm text-gray-900 dark:text-white">{{ channel.name }}</strong><Badge :tone="channelTypeTone(channel.type)">{{ channelTypeLabel(channel.type) }}</Badge></span>
                      <span class="mt-1 block truncate text-xs text-gray-500 dark:text-dark-400">{{ channel.base_url }}</span>
                      <span class="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-gray-500 dark:text-dark-400"><span>{{ formatRelative(channel.last_sync_at) }}</span><span>{{ credentialLabel(channel) }}</span></span>
                    </span>
                    <Badge :tone="channelStatusMeta(channel).tone" dot>{{ channelStatusMeta(channel).label }}</Badge>
                  </button>
                  <p v-if="!activeFiltered.length" class="px-2 py-3 text-xs text-gray-500 dark:text-dark-400">暂无使用中的渠道</p>
                </div>
              </details>
              <details>
                <summary class="flex cursor-pointer list-none items-center justify-between rounded-lg px-2 py-2 text-xs font-semibold text-gray-600 hover:bg-gray-50 dark:text-dark-300 dark:hover:bg-dark-800">
                  <span>已忽略的渠道 <Badge tone="gray">{{ ignoredFiltered.length }}</Badge></span><Icon name="chevronDown" size="xs" />
                </summary>
                <div class="mt-1 space-y-1">
                  <button v-for="channel in ignoredFiltered" :key="channel.id" type="button" class="upstream-rail-item" :class="selectedID === channel.id && 'upstream-rail-item-selected'" @click="selectChannel(channel)">
                    <span class="h-full min-h-14 w-1 flex-shrink-0 rounded-full bg-gray-300 dark:bg-dark-600" />
                    <span class="min-w-0 flex-1 text-left">
                      <span class="flex min-w-0 items-center gap-1.5"><strong class="truncate text-sm text-gray-900 dark:text-white">{{ channel.name }}</strong><Badge :tone="channelTypeTone(channel.type)">{{ channelTypeLabel(channel.type) }}</Badge></span>
                      <span class="mt-1 block truncate text-xs text-gray-500 dark:text-dark-400">{{ channel.base_url }}</span>
                      <span class="mt-2 block text-[11px] text-gray-500 dark:text-dark-400">{{ credentialLabel(channel) }}</span>
                    </span>
                    <Badge tone="gray">已忽略</Badge>
                  </button>
                  <p v-if="!ignoredFiltered.length" class="px-2 py-3 text-xs text-gray-500 dark:text-dark-400">暂无已忽略的渠道</p>
                </div>
              </details>
            </div>
            <EmptyState v-if="!filteredChannels.length" icon="server" title="没有匹配的渠道" description="调整搜索条件或新增渠道。" />
          </div>
        </aside>

        <section class="min-w-0 space-y-4">
          <template v-if="selectedChannel">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="min-w-0 break-words text-xl font-semibold text-gray-900 dark:text-white">{{ selectedChannel.name }}</h2>
                  <Badge :tone="channelTypeTone(selectedChannel.type)">{{ channelTypeLabel(selectedChannel.type) }}</Badge>
                  <Badge :tone="channelStatusMeta(selectedChannel).tone" dot>{{ channelStatusMeta(selectedChannel).label }}</Badge>
                  <Badge v-if="selectedChannel.ignored" tone="gray">已忽略</Badge>
                </div>
                <p class="mt-1 break-all text-sm text-gray-500 dark:text-dark-400">{{ selectedChannel.base_url }}</p>
                <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">最近同步 {{ formatRelative(selectedChannel.last_sync_at) }} · {{ credentialLabel(selectedChannel) }}</p>
              </div>
              <div class="flex flex-wrap items-center justify-end gap-2">
                <button type="button" class="btn btn-secondary btn-sm" @click="openEmailEditor"><Icon name="mail" size="sm" />邮件</button>
                <button type="button" class="btn btn-secondary btn-sm" @click="openWeComEditor"><Icon name="chat" size="sm" />企微</button>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="openChannelEditor(selectedChannel)"><Icon name="edit" size="sm" />编辑</button>
                <button v-if="selectedChannel.type !== 'other'" type="button" class="btn btn-secondary btn-sm" :disabled="busy || selectedChannel.ignored" @click="syncChannel(selectedChannel)"><Icon name="refresh" size="sm" />同步</button>
                <button v-if="selectedChannel.type === 'sub2api'" type="button" class="btn btn-secondary btn-sm" @click="openUpstream(selectedChannel)"><Icon name="externalLink" size="sm" />进入上游</button>
                <button type="button" class="btn btn-ghost btn-icon" :title="selectedChannel.ignored ? '恢复渠道' : '忽略渠道'" @click="setIgnored(selectedChannel, !selectedChannel.ignored)"><Icon :name="selectedChannel.ignored ? 'eye' : 'eyeOff'" size="sm" /></button>
                <button type="button" class="btn btn-ghost btn-icon text-red-600" title="删除渠道" @click="deleteChannel(selectedChannel)"><Icon name="trash" size="sm" /></button>
              </div>
            </div>

            <div v-if="selectedChannel.last_error" class="flex items-start gap-2 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200"><Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" /><span>上次同步发现异常：{{ selectedChannel.last_error }}</span></div>
            <div v-if="detailError" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
              <Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" />
              <span class="min-w-0 flex-1">{{ detailError }}</span>
              <button type="button" class="btn btn-ghost btn-sm" :disabled="detailLoading" @click="loadSelected">重试</button>
            </div>

            <div class="overflow-x-auto">
              <div class="tabs min-w-max">
          <button v-for="tab in detailTabs" :key="tab.value" type="button" class="tab" :class="currentTab === tab.value && 'tab-active'" @click="setTab(tab.value)">
            {{ tab.label }}
          </button>
        </div>
      </div>

      <div v-if="detailLoading && !overview" class="card flex min-h-64 items-center justify-center">
        <div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><span class="spinner text-primary-500" />正在加载渠道详情</div>
      </div>
      <div v-else-if="detailError && !overview" class="card">
        <EmptyState icon="exclamationTriangle" title="渠道详情加载失败" :description="detailError"><button type="button" class="btn btn-secondary btn-sm" @click="loadSelected">重试</button></EmptyState>
      </div>
      <template v-else-if="currentTab === 'overview' && overview">
        <template v-if="isRecordOnly">
          <div class="grid grid-cols-1 gap-3 lg:grid-cols-3">
            <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="database" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">记录型</p><p class="stat-label">渠道用途</p></div></div>
            <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="key" size="lg" /></div><div class="min-w-0"><p class="stat-value truncate text-base">{{ selectedChannel.username || '—' }}</p><p class="stat-label">账号</p></div></div>
            <div class="stat-card"><div class="stat-icon stat-icon-success"><Icon name="checkCircle" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">{{ channelStatusMeta(selectedChannel).label }}</p><p class="stat-label">记录状态</p></div></div>
          </div>
          <section class="card">
            <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">记录信息</h3><p class="mt-1 text-xs text-gray-500 dark:text-dark-400">其它渠道仅保存站点和账号，不执行同步、分组、令牌、余额查询或告警。</p></div>
            <div class="divide-y divide-gray-100 px-6 dark:divide-dark-700">
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-center"><span class="text-sm text-gray-500 dark:text-dark-400">站点</span><strong class="break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.base_url }}</strong></div>
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-center"><span class="text-sm text-gray-500 dark:text-dark-400">账号</span><button v-if="selectedChannel.username" type="button" class="flex min-w-0 items-center gap-2 text-left text-sm text-gray-900 hover:underline dark:text-white" @click="copyText(selectedChannel.username)"><strong class="break-all">{{ selectedChannel.username }}</strong><Icon name="copy" size="xs" /></button><strong v-else class="text-sm text-gray-900 dark:text-white">—</strong></div>
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-center"><span class="text-sm text-gray-500 dark:text-dark-400">密码</span><button v-if="selectedChannel.password" type="button" class="flex min-w-0 items-center gap-2 text-left text-sm text-gray-900 hover:underline dark:text-white" @click="copyText(selectedChannel.password)"><strong class="break-all">{{ selectedChannel.password }}</strong><Icon name="copy" size="xs" /></button><strong v-else class="text-sm text-gray-900 dark:text-white">—</strong></div>
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-center"><span class="text-sm text-gray-500 dark:text-dark-400">创建时间</span><strong class="text-sm text-gray-900 dark:text-white">{{ formatTime(selectedChannel.created_at) }}</strong></div>
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-center"><span class="text-sm text-gray-500 dark:text-dark-400">更新时间</span><strong class="text-sm text-gray-900 dark:text-white">{{ formatTime(selectedChannel.updated_at) }}</strong></div>
              <div class="grid gap-2 py-4 sm:grid-cols-[7rem_1fr] sm:items-start"><span class="text-sm text-gray-500 dark:text-dark-400">充值配置</span><div class="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 text-sm text-gray-700 dark:text-dark-200"><span>比例 {{ rechargeRatioLabel(selectedChannel) }}</span><span v-if="selectedChannel.recharge_methods?.length" class="flex flex-wrap items-center gap-2"><span v-for="method in selectedChannel.recharge_methods" :key="method" class="inline-flex items-center gap-1" :title="rechargeMethodLabel(method)"><RechargeMethodIcon :method="method" size="xs" />{{ rechargeMethodLabel(method) }}</span></span><span v-else>方式未设置</span><span>手续费 {{ selectedChannel.recharge_fee || '无' }}</span></div></div>
            </div>
          </section>
        </template>
        <template v-else>
        <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <div class="stat-card"><div class="stat-icon stat-icon-success"><Icon name="dollar" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">{{ selectedBalance }}</p><p class="stat-label">当前余额</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="key" size="lg" /></div><div class="min-w-0"><p class="stat-value">{{ tokenRows.length }}</p><p class="stat-label">令牌</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="grid" size="lg" /></div><div class="min-w-0"><p class="stat-value">{{ groupRows.length }}</p><p class="stat-label">分组</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-warning"><Icon name="clock" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">{{ formatRelative(selectedChannel.last_sync_at) }}</p><p class="stat-label">上次同步</p></div></div>
        </div>

          <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <section class="card">
            <div class="card-header flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 class="font-semibold text-gray-900 dark:text-white">余额趋势</h3>
                <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">最近 {{ chartHistory.length }} 个快照</p>
              </div>
              <div v-if="chartHistory.length > 1" class="text-right">
                <p class="text-xs text-gray-500 dark:text-dark-400">{{ chartTrend.label }}</p>
                <p class="text-sm font-semibold" :class="chartTrend.className">
                  {{ chartDelta > 0 ? '+' : chartDelta < 0 ? '-' : '' }}{{ formatNumber(Math.abs(chartDelta)) }}<span v-if="chartDeltaPercent !== null" class="ml-1 text-xs font-normal">({{ chartDeltaPercent > 0 ? '+' : chartDeltaPercent < 0 ? '-' : '' }}{{ Math.abs(chartDeltaPercent).toFixed(1) }}%)</span>
                </p>
              </div>
            </div>
            <div class="card-body">
              <div v-if="chartPoints" class="relative outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40" tabindex="0" @pointermove="updateChartHover" @pointerleave="chartHoverIndex = -1" @focus="chartHoverIndex = chartCoordinates.length - 1" @blur="chartHoverIndex = -1">
                <svg viewBox="0 0 600 180" class="aspect-[10/3] w-full" role="img" aria-label="最近余额趋势">
                  <line x1="20" y1="30" x2="580" y2="30" class="stroke-gray-100 dark:stroke-dark-700" />
                  <line x1="20" y1="90" x2="580" y2="90" class="stroke-gray-100 dark:stroke-dark-700" />
                  <line x1="20" y1="150" x2="580" y2="150" class="stroke-gray-200 dark:stroke-dark-600" />
                  <polyline :points="chartPoints" fill="none" class="stroke-primary-500" stroke-width="3" stroke-linejoin="round" stroke-linecap="round" />
                  <circle v-if="chartHoverSnapshot" :cx="chartCoordinates[chartHoverIndex >= 0 ? chartHoverIndex : chartCoordinates.length - 1]?.x" :cy="chartCoordinates[chartHoverIndex >= 0 ? chartHoverIndex : chartCoordinates.length - 1]?.y" r="5" class="fill-white stroke-primary-500" stroke-width="3" />
                </svg>
                <div class="mt-2 flex items-center justify-between gap-3 text-xs text-gray-500 dark:text-dark-400">
                  <span>{{ formatTime(chartHistory[0]?.captured_at) }}</span>
                  <span v-if="chartHoverSnapshot" class="min-w-0 truncate text-center font-medium text-gray-700 dark:text-dark-200">{{ formatNumber(chartHoverSnapshot.balance) }} · {{ formatTime(chartHoverSnapshot.captured_at) }}</span>
                  <span>{{ formatTime(chartHistory[chartHistory.length - 1]?.captured_at) }}</span>
                </div>
              </div>
              <EmptyState v-else icon="chart" title="暂无余额历史" description="同步渠道后会记录余额快照。" />
            </div>
          </section>
          <section class="card">
            <div class="card-header flex flex-wrap items-center justify-between gap-2">
              <div class="min-w-0"><h3 class="font-semibold text-gray-900 dark:text-white">连接凭据</h3><p class="truncate text-xs text-gray-500 dark:text-dark-400">{{ selectedChannel.base_url }}</p></div>
              <Badge :tone="channelTypeTone(selectedChannel.type)">{{ channelTypeLabel(selectedChannel.type) }}</Badge>
            </div>
            <div class="divide-y divide-gray-100 px-6 dark:divide-dark-700">
              <div v-if="selectedChannel.type !== 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[6rem_1fr_auto] sm:items-center">
                <span class="text-sm text-gray-500 dark:text-dark-400">账号</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.username || '—' }}</code>
                <button type="button" class="btn btn-ghost btn-icon" title="复制账号" @click="copyText(selectedChannel.username)"><Icon name="copy" size="sm" /></button>
              </div>
              <div v-if="selectedChannel.type !== 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[6rem_1fr_auto] sm:items-center">
                <span class="text-sm text-gray-500 dark:text-dark-400">密码</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.password || '—' }}</code>
                <button type="button" class="btn btn-ghost btn-icon" title="复制密码" @click="copyText(selectedChannel.password)"><Icon name="copy" size="sm" /></button>
              </div>
              <div v-if="selectedChannel.type === 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[6rem_1fr_auto] sm:items-center">
                <span class="text-sm text-gray-500 dark:text-dark-400">用户 ID</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.newapi_user_id }}</code>
                <button type="button" class="btn btn-ghost btn-icon" title="复制用户 ID" @click="copyText(selectedChannel.newapi_user_id)"><Icon name="copy" size="sm" /></button>
              </div>
              <div v-if="selectedChannel.type === 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[6rem_1fr_auto] sm:items-center">
                <span class="text-sm text-gray-500 dark:text-dark-400">系统令牌</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.newapi_access_token }}</code>
                <button type="button" class="btn btn-ghost btn-icon" title="复制系统令牌" @click="copyText(selectedChannel.newapi_access_token)"><Icon name="copy" size="sm" /></button>
              </div>
            </div>
            <div class="border-t border-gray-100 p-4 dark:border-dark-700">
              <div class="flex flex-wrap items-center gap-x-5 gap-y-3 text-sm">
                <span class="font-medium text-gray-700 dark:text-dark-200">充值配置</span>
                <span class="text-gray-500 dark:text-dark-400">比例 <strong class="text-gray-900 dark:text-white">{{ rechargeRatioLabel(selectedChannel) }}</strong></span>
                <span v-if="selectedChannel.recharge_methods?.length" class="flex items-center gap-2 text-gray-500 dark:text-dark-400">
                  方式
                  <span v-for="method in selectedChannel.recharge_methods" :key="method" class="inline-flex items-center gap-1 text-gray-700 dark:text-dark-200" :title="rechargeMethodLabel(method)">
                    <RechargeMethodIcon :method="method" size="xs" />
                    <span>{{ rechargeMethodLabel(method) }}</span>
                  </span>
                </span>
                <span v-else class="text-gray-500 dark:text-dark-400">方式 <strong class="text-gray-900 dark:text-white">未设置</strong></span>
                <span class="min-w-0 text-gray-500 dark:text-dark-400">手续费 <strong class="break-all text-gray-900 dark:text-white">{{ selectedChannel.recharge_fee || '无' }}</strong></span>
              </div>
            </div>
          </section>
        </div>

        <section class="card">
          <div class="card-header">
            <h3 class="font-semibold text-gray-900 dark:text-white">账户资料</h3>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">渠道返回的用户资料缓存</p>
          </div>
          <div class="card-body">
            <JsonTable
              :data="overview?.profile ? [overview.profile] : []"
              :column-order="profileColumnOrder"
              empty-text="暂无账户资料缓存"
            />
          </div>
        </section>

        <div class="grid grid-cols-1 gap-4">
        <section v-if="selectedChannel.type !== 'other'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">分组</h3></div>
          <div class="p-4">
            <JsonTable
              :data="overview?.groups"
              :column-order="groupColumnOrder"
              empty-text="暂无分组缓存"
              table-class="group-cache-table"
            />
          </div>
        </section>

        <section v-if="selectedChannel.type !== 'other'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">令牌</h3></div>
          <div class="table-container m-4">
            <table class="table">
              <thead><tr><th v-for="column in leadingTokenColumns" :key="column">{{ tokenColumnLabel(column) }}</th><th>分组</th><th>倍率</th><th>模型</th><th v-for="column in trailingTokenColumns" :key="column">{{ tokenColumnLabel(column) }}</th></tr></thead>
              <tbody>
                <tr v-for="(token, index) in tokenRows" :key="rowKey(token, index)">
                  <td v-for="column in leadingTokenColumns" :key="column" :title="tokenPreview(token[column])">
                    <button v-if="column === 'key'" type="button" class="flex min-w-0 max-w-full items-center gap-1 text-left text-primary-600 hover:underline dark:text-primary-400" @click="copyText(String(token[column] ?? ''))"><span class="truncate">{{ tokenPreview(token[column]) }}</span><Icon name="copy" size="xs" /></button>
                    <span v-else :class="column === 'name' && 'font-medium'">{{ tokenValue(token, column, index) }}</span>
                  </td>
                  <td>
                    <select
                      v-if="tokenOptions(token).length"
                      class="input min-w-[8rem] py-1.5 text-xs"
                      :value="tokenGroupValue(token)"
                      :disabled="busy || !tokenID(token)"
                      aria-label="令牌分组"
                      @change="changeTokenGroup(token, $event)"
                    >
                      <option v-if="selectedChannel.type === 'sub2api' && !tokenGroupValue(token)" value="">未设置</option>
                      <option v-for="option in tokenOptions(token)" :key="option.value" :value="option.value">{{ option.label }}</option>
                    </select>
                    <span v-else>{{ tokenGroupLabel(token) }}</span>
                  </td>
                  <td>
                    <span class="inline-flex items-center gap-0.5">
                      {{ tokenRatioLabel(selectedChannel, token) }}
                      <RatioChangeIndicator :change="tokenRatioChange(selectedChannel, token)" />
                    </span>
                  </td>
                  <td><button type="button" class="btn btn-ghost btn-icon" title="查看模型" :disabled="!tokenID(token)" @click="showTokenModels(token)"><Icon name="search" size="sm" /></button></td>
                  <td v-for="column in trailingTokenColumns" :key="column">{{ tokenValue(token, column, index) }}</td>
                </tr>
              </tbody>
            </table>
            <EmptyState v-if="!tokenRows.length" icon="key" title="暂无令牌" description="上游没有返回令牌。" />
          </div>
        </section>
        </div>

        <section v-if="selectedChannel.type === 'sub2api'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">订阅</h3></div>
          <div class="p-4"><JsonTable :data="activeSubscriptionRows" empty-text="当前无订阅" /></div>
        </section>
        </template>
      </template>

      <template v-else-if="currentTab === 'automation'">
        <div v-if="tabError" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"><Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" /><span class="min-w-0 flex-1">{{ tabError }}</span><button type="button" class="btn btn-ghost btn-sm" @click="loadTabData">重试</button></div>
        <div v-if="tabLoading && !tasks.length" class="card flex min-h-40 items-center justify-center"><div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><span class="spinner text-primary-500" />正在加载自动任务</div></div>
        <div v-else class="grid grid-cols-1 gap-4 2xl:grid-cols-[minmax(18rem,0.8fr)_minmax(0,1.2fr)]">
          <section class="card p-5">
            <div class="mb-4"><h3 class="font-semibold text-gray-900 dark:text-white">新建自动化</h3><p class="mt-1 text-xs text-gray-500 dark:text-dark-400">按余额、消耗速度或分组变化触发告警。</p></div>
            <div class="grid grid-cols-2 gap-2 sm:grid-cols-3 2xl:grid-cols-2">
              <button v-for="type in taskTypes" :key="type" type="button" class="btn btn-secondary btn-sm" :class="taskEditor.type === type && 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'" @click="selectTaskType(type)">{{ taskLabel(type) }}</button>
            </div>
            <div class="mt-4 space-y-4">
              <Field v-if="!isGroupTask(taskEditor.type)" v-model="taskEditor.threshold" label="阈值" type="number" :step="0.0001" :placeholder="taskEditor.type === 'burn_rate' ? '每小时消耗量' : '余额上限'" />
              <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field v-model="taskEditor.interval" label="检查间隔" type="number" :min="1" suffix="分钟" />
                <Field v-if="taskEditor.type === 'burn_rate'" v-model="taskEditor.lookback" label="统计窗口" type="number" :min="1" suffix="分钟" />
                <Field v-model="taskEditor.cooldown" label="告警冷却" type="number" :min="0" suffix="分钟" />
              </div>
              <Field v-model="taskEditor.recipients" label="邮件收件人" hint="留空使用邮件默认收件人，多个邮箱使用逗号分隔；企微使用全局设置的接收人" />
              <p class="input-hint">{{ taskHint(taskEditor.type) }}</p>
              <button type="button" class="btn btn-primary w-full" :disabled="busy || selectedChannel.type === 'other'" @click="createTaskInline"><Icon name="plus" size="sm" />新建任务</button>
            </div>
          </section>
          <section class="card">
            <div class="card-header flex items-center justify-between gap-2"><div><h3 class="font-semibold text-gray-900 dark:text-white">任务列表</h3><p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ selectedChannel.name }} 的自动化规则</p></div><Badge tone="gray">{{ tasks.length }} 个任务</Badge></div>
          <div class="table-container m-4">
            <table class="table">
              <thead><tr><th>任务</th><th>间隔</th><th>阈值</th><th>冷却</th><th>最近运行</th><th>启用</th><th class="text-right">操作</th></tr></thead>
              <tbody>
                <tr v-for="task in tasks" :key="task.id"><td><Badge :tone="taskTone(task.type)">{{ taskLabel(task.type) }}</Badge></td><td>{{ task.interval_minutes }} 分钟</td><td>{{ task.type.startsWith('group_') ? '—' : task.threshold }}</td><td>{{ task.cooldown_minutes }} 分钟</td><td>{{ formatRelative(task.last_run_at) }}</td><td><Toggle :model-value="task.enabled" :disabled="busy" @update:model-value="toggleTask(task, $event)" /></td><td><div class="flex justify-end gap-1"><button type="button" class="btn btn-ghost btn-icon" title="编辑任务" @click="openTaskEditor(task)"><Icon name="edit" size="sm" /></button><button type="button" class="btn btn-ghost btn-icon text-red-600" title="删除任务" @click="deleteTask(task)"><Icon name="trash" size="sm" /></button></div></td></tr>
              </tbody>
            </table>
            <EmptyState v-if="!tasks.length" icon="bell" title="暂无自动任务" description="新增余额或分组告警任务。" />
          </div>
          </section>
        </div>
      </template>

      <template v-else-if="currentTab === 'balance-logs'">
        <div v-if="tabError" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"><Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" /><span class="min-w-0 flex-1">{{ tabError }}</span><button type="button" class="btn btn-ghost btn-sm" @click="loadTabData">重试</button></div>
        <div v-if="tabLoading && !balanceLogs.length" class="card flex min-h-40 items-center justify-center"><div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><span class="spinner text-primary-500" />正在加载余额日志</div></div>
        <section v-else class="card">
          <div class="table-container m-4">
            <table class="table"><thead><tr><th>时间</th><th>状态</th><th>余额</th><th>已用</th><th>信息</th></tr></thead><tbody><tr v-for="item in balanceLogs" :key="item.id"><td class="whitespace-nowrap">{{ formatTime(item.created_at) }}</td><td><Badge :tone="item.status === 'success' ? 'success' : 'danger'">{{ item.status === 'success' ? '成功' : '失败' }}</Badge></td><td>{{ item.balance === undefined ? '—' : formatNumber(item.balance) }}</td><td>{{ item.used_balance === undefined ? '—' : formatNumber(item.used_balance) }}</td><td class="max-w-md break-words">{{ item.error || item.message }}</td></tr></tbody></table>
            <EmptyState v-if="!balanceLogs.length" icon="document" title="暂无余额查询日志" description="同步或自动检查后会生成日志。" />
          </div>
          <div class="px-6 pb-4"><MiniPager :page="logPage" :page-size="logPageSize" :total="logTotal" @update:page="changeLogPage" /></div>
        </section>
      </template>

      <template v-else>
        <div v-if="tabError" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"><Icon name="exclamationTriangle" size="sm" class="mt-0.5 flex-shrink-0" /><span class="min-w-0 flex-1">{{ tabError }}</span><button type="button" class="btn btn-ghost btn-sm" @click="loadTabData">重试</button></div>
        <div v-if="tabLoading && !alerts.length" class="card flex min-h-40 items-center justify-center"><div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400"><span class="spinner text-primary-500" />正在加载告警</div></div>
        <section v-else class="card">
          <div class="table-container m-4">
            <table class="table"><thead><tr><th>时间</th><th>类型</th><th>消息</th><th>邮件</th><th>企微</th></tr></thead><tbody><tr v-for="alert in alerts" :key="alert.id"><td class="whitespace-nowrap">{{ formatTime(alert.created_at) }}</td><td><Badge :tone="taskTone(alert.type)">{{ taskLabel(alert.type) }}</Badge></td><td class="max-w-xl break-words">{{ alert.message }}</td><td><Badge :tone="alert.email_sent ? 'success' : alert.email_error ? 'danger' : 'gray'">{{ alert.email_sent ? '已发送' : alert.email_error || '未发送' }}</Badge></td><td><Badge :tone="alert.wecom_sent ? 'success' : alert.wecom_error ? 'danger' : 'gray'">{{ alert.wecom_sent ? '已发送' : alert.wecom_error || '未发送' }}</Badge></td></tr></tbody></table>
            <EmptyState v-if="!alerts.length" icon="bell" title="暂无告警事件" description="任务达到阈值后会记录事件。" />
          </div>
        </section>
      </template>
          </template>
          <div v-else class="card">
            <EmptyState icon="server" title="暂无渠道" description="从左侧新增渠道后，可在这里查看详细信息。" />
          </div>
        </section>
      </div>
    </template>

    <Modal :open="channelEditor.open" :title="channelEditor.id ? '编辑上游渠道' : '新增上游渠道'" @close="channelEditor.open = false">
      <div class="space-y-4">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field v-model="channelEditor.name" label="名称" placeholder="留空使用站点域名" />
          <label class="block"><span class="input-label">类型</span><select v-model="channelEditor.type" class="input" :disabled="Boolean(channelEditor.id)"><option value="sub2api">Sub2API</option><option value="newapi">New API</option><option value="other">其它</option></select></label>
        </div>
        <Field v-model="channelEditor.baseURL" label="站点地址" placeholder="https://example.com" />
        <div v-if="channelEditor.type !== 'newapi'" class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="channelEditor.username" label="账号 / 邮箱" /><Field v-model="channelEditor.password" label="密码" type="password" :placeholder="channelEditor.id ? '留空保持不变' : ''" /></div>
        <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="channelEditor.newAPIUserID" label="用户 ID" /><Field v-model="channelEditor.newAPIAccessToken" label="系统访问令牌" type="password" :placeholder="channelEditor.id ? '留空保持不变' : ''" /></div>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field v-model="channelEditor.rechargeRatio" label="充值比例" type="number" :min="0.0001" :step="0.01" suffix="1 : x" hint="默认 1:1，例如 1.2 表示充值 1 元到账 1.2 元。" />
          <Field v-model="channelEditor.rechargeFee" label="充值手续费" placeholder="例如：每笔 2 元，留空表示无" />
        </div>
        <div>
          <span class="input-label">充值方式</span>
          <div class="grid grid-cols-3 gap-2">
            <label v-for="option in rechargeMethodOptions" :key="option.value" class="flex cursor-pointer items-center gap-2 rounded-xl border border-gray-200 bg-white px-3 py-2.5 text-sm text-gray-700 transition-colors hover:border-primary-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200">
              <input type="checkbox" class="h-4 w-4 accent-primary-600" :checked="channelEditor.rechargeMethods.includes(option.value)" @change="toggleRechargeMethod(option.value)" />
              <RechargeMethodIcon :method="option.value" size="sm" />
              <span>{{ option.label }}</span>
            </label>
          </div>
          <p class="input-hint">默认不勾选；汇总卡片会在右上角显示已勾选方式。</p>
        </div>
        <SwitchRow v-model="channelEditor.ignored" label="忽略渠道" description="忽略后不执行自动同步或告警。" />
        <SwitchRow v-if="channelEditor.id && channelEditor.type !== 'other'" v-model="channelEditor.sync" label="保存后同步" />
      </div>
      <template #footer><button type="button" class="btn btn-secondary" @click="channelEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveChannel">保存</button></template>
    </Modal>

    <Modal :open="emailEditor.open" title="邮件设置" @close="emailEditor.open = false">
      <div class="space-y-4">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="emailEditor.host" label="SMTP 主机" /><Field v-model="emailEditor.port" label="端口" type="number" :min="1" :max="65535" /><Field v-model="emailEditor.user" label="用户名" /><Field v-model="emailEditor.password" label="密码" type="password" :placeholder="emailEditor.hasPassword ? '已配置，留空保持不变' : ''" /></div>
        <Field v-model="emailEditor.from" label="发件人" placeholder="Guardian <guardian@example.com>" />
        <Field v-model="emailEditor.subjectPrefix" label="主题前缀" placeholder="[Guardian] " />
        <Field v-model="emailEditor.recipients" label="默认收件人" hint="多个邮箱使用逗号分隔" />
        <Field v-model="emailEditor.testRecipient" label="测试收件人" hint="留空使用默认收件人，多个邮箱使用逗号分隔" />
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="emailEditor.interval" label="默认检查间隔" type="number" :min="1" suffix="分钟" /><SwitchRow v-model="emailEditor.secure" label="隐式 TLS" description="通常用于 465 端口。" /></div>
      </div>
      <template #footer><button type="button" class="btn btn-secondary" :disabled="busy" @click="testEmail"><Icon name="mail" size="sm" />测试</button><button type="button" class="btn btn-secondary" @click="emailEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveEmail">保存</button></template>
    </Modal>

    <Modal :open="wecomEditor.open" title="企微应用通知" @close="wecomEditor.open = false">
      <div class="space-y-4">
        <Field v-model="wecomEditor.corpID" label="企业 ID（CorpID，ww 开头）" placeholder="wwxxxxxxxxxxxxxxxx" hint="你提供的 ww714e174dee3d85eb 属于这里。" />
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field v-model="wecomEditor.agentID" label="应用 AgentId（纯数字）" type="text" inputmode="numeric" placeholder="例如 1000001" hint="在应用详情中查看；不要填以 ww 开头的 CorpID。" />
          <Field v-model="wecomEditor.secret" label="应用 Secret" type="text" placeholder="请输入应用 Secret" hint="Secret 按明文显示；留空提交不会覆盖已保存值。" />
        </div>
        <Field v-model="wecomEditor.target" label="企微接收人" placeholder="zhangsan 或 @all" hint="多个成员 ID 使用 | 分隔；也可以填 @all。" />
        <p class="input-hint">Guardian 直接调用企业微信官方应用接口。</p>
      </div>
      <template #footer><button type="button" class="btn btn-secondary" :disabled="busy" @click="testWeCom"><Icon name="chat" size="sm" />测试</button><button type="button" class="btn btn-secondary" @click="wecomEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveWeCom">保存</button></template>
    </Modal>

    <Modal :open="taskEditor.open" :title="taskEditor.id ? '编辑自动任务' : '新增自动任务'" @close="taskEditor.open = false">
      <div class="space-y-4">
        <label class="block"><span class="input-label">任务类型</span><select v-model="taskEditor.type" class="input"><option v-for="type in taskTypes" :key="type" :value="type">{{ taskLabel(type) }}</option></select></label>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="taskEditor.interval" label="检查间隔" type="number" :min="1" suffix="分钟" /><Field v-if="!taskEditor.type.startsWith('group_')" v-model="taskEditor.threshold" label="阈值" type="number" :step="0.01" /><Field v-if="taskEditor.type === 'burn_rate'" v-model="taskEditor.lookback" label="观察窗口" type="number" :min="1" suffix="分钟" /><Field v-model="taskEditor.cooldown" label="告警冷却" type="number" :min="0" suffix="分钟" /></div>
        <Field v-model="taskEditor.recipients" label="邮件收件人" hint="留空使用邮件默认收件人，多个邮箱使用逗号分隔；企微使用全局设置的接收人" />
        <SwitchRow v-model="taskEditor.enabled" label="启用任务" />
      </div>
      <template #footer><button type="button" class="btn btn-secondary" @click="taskEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveTask">保存</button></template>
    </Modal>

    <Modal :open="modelsModal.open" :title="`令牌模型 · ${modelsModal.name || modelsModal.id}`" @close="modelsModal.open = false">
      <div v-if="modelsModal.loading" class="flex justify-center py-12"><span class="spinner text-primary-500" /></div>
      <div v-else class="flex flex-wrap gap-2"><Badge v-for="model in modelsModal.models" :key="model" tone="primary">{{ model }}</Badge><EmptyState v-if="!modelsModal.models.length" icon="cube" title="没有返回模型" description="令牌和上游模型接口均未返回可用模型。" /></div>
    </Modal>

  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import EmptyState from '@/components/EmptyState.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import JsonTable from '@/components/JsonTable.vue'
import MiniPager from '@/components/MiniPager.vue'
import Modal from '@/components/Modal.vue'
import RatioChangeIndicator from '@/components/RatioChangeIndicator.vue'
import RechargeMethodIcon from '@/components/RechargeMethodIcon.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import Toggle from '@/components/Toggle.vue'
import { api } from '@/lib/api'
import { formatRelative, formatTime } from '@/lib/format'
import type {
  UpstreamAlert,
  UpstreamAutomationTask,
  UpstreamBalanceLog,
  UpstreamChannel,
  UpstreamChannelType,
  UpstreamOverview,
  UpstreamRechargeMethod,
  UpstreamTaskType
} from '@/lib/types'
import { useUIStore } from '@/stores/ui'

type DetailTab = 'overview' | 'automation' | 'balance-logs' | 'alerts'
type Row = Record<string, unknown>

const route = useRoute()
const router = useRouter()
const ui = useUIStore()
const channels = ref<UpstreamChannel[]>([])
const overview = ref<UpstreamOverview | null>(null)
const tasks = ref<UpstreamAutomationTask[]>([])
const balanceLogs = ref<UpstreamBalanceLog[]>([])
const alerts = ref<UpstreamAlert[]>([])
const busy = ref(false)
const syncingID = ref(0)
const search = ref('')
const summaryOverviews = ref<Record<number, UpstreamOverview>>({})
const summaryErrors = ref<Record<number, string>>({})
const summaryLoading = ref(false)
const summaryError = ref('')
const detailLoading = ref(false)
const detailError = ref('')
const tabError = ref('')
const tabLoading = ref(false)
const logPage = ref(1)
const logPageSize = ref(20)
const logTotal = ref(0)
const chartHoverIndex = ref(-1)

const isSummaryPage = computed(() => route.name === 'upstream-channel-summary')
const selectedID = computed(() => Number(route.query.id) || 0)
const selectedChannel = computed(() => channels.value.find(item => item.id === selectedID.value) ?? null)
const currentTab = computed<DetailTab>(() => {
  const value = String(route.query.tab || 'overview')
  if (selectedChannel.value?.type === 'other') return 'overview'
  return ['overview', 'automation', 'balance-logs', 'alerts'].includes(value) ? (value as DetailTab) : 'overview'
})
const allDetailTabs: Array<{ value: DetailTab; label: string }> = [
  { value: 'overview', label: '概览' },
  { value: 'automation', label: '自动化' },
  { value: 'balance-logs', label: '余额日志' },
  { value: 'alerts', label: '告警' }
]
const detailTabs = computed(() => selectedChannel.value?.type === 'other' ? allDetailTabs.slice(0, 1) : allDetailTabs)
const isRecordOnly = computed(() => selectedChannel.value?.type === 'other')
const filteredChannels = computed(() => {
  const needle = search.value.trim().toLowerCase()
  if (!needle) return channels.value
  return channels.value.filter(channel => [channel.name, channel.type, channel.base_url, channel.username, channel.newapi_user_id].join(' ').toLowerCase().includes(needle))
})
const activeFiltered = computed(() => filteredChannels.value.filter(channel => !channel.ignored))
const ignoredFiltered = computed(() => filteredChannels.value.filter(channel => channel.ignored))
const syncableChannels = computed(() => channels.value.filter(channel => !channel.ignored && channel.type !== 'other'))
const summaryGroups = computed(() => {
  const byName = (items: UpstreamChannel[]) => [...items].sort((a, b) => a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' }))
  return [
    { key: 'active', label: '使用中的渠道', items: byName(channels.value.filter(channel => !channel.ignored)) },
    { key: 'ignored', label: '已忽略的渠道', items: byName(channels.value.filter(channel => channel.ignored)) }
  ]
})
const groupRows = computed(() => toRows(overview.value?.groups))
const tokenRows = computed(() => toRows(overview.value?.tokens))
const activeSubscriptionRows = computed(() => {
  const subscriptions = overview.value?.subscriptions
  if (isRow(subscriptions) && 'active' in subscriptions) return toRows(subscriptions.active)
  return toRows(subscriptions)
})
const profileColumnOrder = computed(() => selectedChannel.value?.type === 'newapi'
  ? ['id', 'username', 'display_name', 'role', 'status', 'email', 'quota', 'used_quota']
  : ['id', 'email', 'username', 'role', 'balance', 'concurrency', 'status', 'allowed_groups'])
const groupColumnOrder = computed(() => selectedChannel.value?.type === 'newapi'
  ? ['name', 'ratio', 'rate', 'rate_multiplier', 'description', 'platform', 'status', 'id']
  : ['id', 'name', 'description', 'platform', 'rate_multiplier', 'is_exclusive', 'status', 'subscription_type'])
const tokenDisplayColumns = computed(() => {
  const available = new Set(tokenRows.value.flatMap(row => Object.keys(row)))
  return ['id', 'name', 'key', 'status', 'remain_quota', 'used_quota', 'expired_time', 'expires_at']
    .filter(column => available.has(column))
})
const leadingTokenColumns = computed(() => tokenDisplayColumns.value.slice(0, 4))
const trailingTokenColumns = computed(() => tokenDisplayColumns.value.slice(4))
const selectedBalance = computed(() => {
  const snapshot = overview.value?.latest_snapshot
  return snapshot ? formatNumber(snapshot.balance) : '—'
})
const chartHistory = computed(() => (overview.value?.history ?? []).slice(-30))
const chartCoordinates = computed(() => {
  const history = chartHistory.value
  if (!history.length) return []
  const values = history.map(item => item.balance)
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = max - min || 1
  return values.map((value, index) => {
    const x = history.length === 1 ? 300 : 20 + (index / (history.length - 1)) * 560
    const y = 150 - ((value - min) / span) * 120
    return { x, y, item: history[index] }
  })
})
const chartPoints = computed(() => chartCoordinates.value.map(point => `${point.x.toFixed(1)},${point.y.toFixed(1)}`).join(' '))
const chartHoverSnapshot = computed(() => {
  const points = chartCoordinates.value
  if (!points.length) return undefined
  return points[chartHoverIndex.value >= 0 ? chartHoverIndex.value : points.length - 1]?.item
})
const chartDelta = computed(() => {
  const points = chartCoordinates.value
  if (points.length < 2) return 0
  return points[points.length - 1].item.balance - points[0].item.balance
})
const chartDeltaPercent = computed(() => {
  const first = chartCoordinates.value[0]?.item.balance
  return first ? (chartDelta.value / Math.abs(first)) * 100 : null
})
const chartTrend = computed(() => chartDelta.value > 0
  ? { label: '增加', className: 'text-emerald-600 dark:text-emerald-400' }
  : chartDelta.value < 0
    ? { label: '消耗', className: 'text-red-600 dark:text-red-400' }
    : { label: '持平', className: 'text-gray-600 dark:text-dark-300' })
const groupOptions = computed(() => {
  const options: Array<{ value: string; label: string }> = []
  const append = (value: string, label: string) => {
    if (!options.some(option => option.value === value)) options.push({ value, label })
  }
  if (selectedChannel.value?.type === 'newapi') {
    append('', '默认分组')
    groupRows.value.forEach(group => {
      const name = groupName(group)
      if (name) append(name, name)
    })
    tokenRows.value.forEach(token => {
      const value = tokenGroupValue(token, 'newapi')
      if (value) append(value, value)
    })
    return options
  }
  groupRows.value.forEach(group => {
    const value = String(firstValue(group, ['id', 'ID', 'group_id']) ?? '')
    if (value) append(value, groupName(group) || value)
  })
  tokenRows.value.forEach(token => {
    const value = tokenGroupValue(token, selectedChannel.value?.type)
    if (!value) return
    const group = firstValue(token, ['group', 'Group'])
    append(value, isRow(group) ? groupName(group) || value : value)
  })
  return options
})

const rechargeMethodOptions: Array<{ value: UpstreamRechargeMethod; label: string }> = [
  { value: 'alipay', label: '支付宝' },
  { value: 'wechat', label: '微信' },
  { value: 'card', label: '卡网' }
]
const channelEditor = reactive({ open: false, id: 0, name: '', type: 'sub2api' as UpstreamChannelType, baseURL: '', username: '', password: '', newAPIAccessToken: '', newAPIUserID: '', rechargeRatio: 1, rechargeMethods: [] as UpstreamRechargeMethod[], rechargeFee: '', ignored: false, sync: true })
const emailEditor = reactive({ open: false, host: '', port: 587, secure: false, user: '', password: '', from: '', subjectPrefix: '', recipients: '', testRecipient: '', interval: 30, hasPassword: false })
const wecomEditor = reactive({ open: false, corpID: '', agentID: '', secret: '', target: '', hasSecret: false })
const taskTypes: UpstreamTaskType[] = ['low_balance', 'burn_rate', 'group_added', 'group_removed', 'group_ratio_changed']
const taskEditor = reactive({ open: false, id: 0, type: 'low_balance' as UpstreamTaskType, enabled: true, interval: 5, threshold: 10, lookback: 60, cooldown: 30, recipients: '' })
const modelsModal = reactive({ open: false, loading: false, id: 0, name: '', models: [] as string[] })

onMounted(async () => {
  await loadChannels()
  await loadSelected()
})

watch(selectedID, async (next, previous) => {
  if (next !== previous) await loadSelected()
})

watch(isSummaryPage, async (next) => {
  if (next) await loadSummaryOverviews()
})

watch(currentTab, async () => {
  await loadTabData()
})

async function loadChannels() {
  if (isSummaryPage.value) {
    summaryLoading.value = true
    summaryError.value = ''
  }
  try {
    const data = await api.upstreamChannels()
    channels.value = data.items
    if (selectedID.value && !channels.value.some(item => item.id === selectedID.value)) {
      await goBack()
      return
    }
    if (!isSummaryPage.value && !selectedID.value && channels.value.length) {
      await router.replace({ path: '/upstream-channels/list', query: { id: String(channels.value[0].id), tab: 'overview' } })
    }
    if (isSummaryPage.value) {
      if (channels.value.length) await loadSummaryOverviews()
      else summaryLoading.value = false
    }
  } catch (err) {
    if (isSummaryPage.value) summaryError.value = (err as Error).message
    if (isSummaryPage.value) summaryLoading.value = false
    ui.notify('error', (err as Error).message)
  }
}

async function loadSummaryOverviews() {
  if (!isSummaryPage.value || !channels.value.length) {
    summaryOverviews.value = {}
    summaryErrors.value = {}
    summaryLoading.value = false
    return
  }
  summaryLoading.value = true
  summaryError.value = ''
  try {
    const results = await Promise.allSettled(channels.value.map(channel => api.upstreamOverview(channel.id)))
    const next: Record<number, UpstreamOverview> = {}
    const errors: Record<number, string> = {}
    results.forEach((result, index) => {
      const id = channels.value[index].id
      if (result.status === 'fulfilled') next[id] = result.value
      else errors[id] = result.reason instanceof Error ? result.reason.message : '汇总数据加载失败'
    })
    summaryOverviews.value = next
    summaryErrors.value = errors
    summaryError.value = Object.keys(errors).length ? `${Object.keys(errors).length} 个渠道汇总加载失败` : ''
  } catch (err) {
    summaryError.value = (err as Error).message
  } finally {
    summaryLoading.value = false
  }
}

async function loadSelected() {
  overview.value = null
  tasks.value = []
  balanceLogs.value = []
  alerts.value = []
  detailError.value = ''
  tabError.value = ''
  detailLoading.value = false
  if (!selectedID.value) return
  if (selectedChannel.value?.type === 'other' && route.query.tab !== 'overview') {
    await router.replace({ query: { id: String(selectedID.value), tab: 'overview' } })
  }
  detailLoading.value = true
  try {
    overview.value = await api.upstreamOverview(selectedID.value)
    await loadTabData()
  } catch (err) {
    detailError.value = (err as Error).message
  } finally {
    detailLoading.value = false
  }
}

async function loadTabData() {
  if (!selectedID.value) return
  tabError.value = ''
  tabLoading.value = true
  try {
    if (currentTab.value === 'automation') tasks.value = (await api.upstreamTasks(selectedID.value)).items
    if (currentTab.value === 'balance-logs') await loadBalanceLogs(logPage.value)
    if (currentTab.value === 'alerts') alerts.value = (await api.upstreamAlerts(selectedID.value)).items
  } catch (err) {
    tabError.value = (err as Error).message
  } finally {
    tabLoading.value = false
  }
}

async function loadBalanceLogs(page: number) {
  const data = await api.upstreamBalanceLogs(selectedID.value, page)
  balanceLogs.value = data.items
  logPage.value = data.page
  logPageSize.value = data.page_size
  logTotal.value = data.total
}

async function changeLogPage(page: number) {
  tabLoading.value = true
  tabError.value = ''
  try {
    await loadBalanceLogs(page)
  } catch (err) {
    tabError.value = (err as Error).message
    ui.notify('error', tabError.value)
  } finally {
    tabLoading.value = false
  }
}

function selectChannel(channel: UpstreamChannel) {
  void router.push({ path: '/upstream-channels/list', query: { id: String(channel.id), tab: 'overview' } })
}

async function goBack() {
  await router.push({ path: '/upstream-channels/list' })
}

function setTab(tab: DetailTab) {
  logPage.value = 1
  void router.replace({ query: { id: String(selectedID.value), tab } })
}

async function syncChannel(channel: UpstreamChannel) {
  busy.value = true
  syncingID.value = channel.id
  try {
    await api.syncUpstreamChannel(channel.id)
    await loadChannels()
    if (selectedID.value === channel.id) await loadSelected()
    ui.notify('success', `「${channel.name}」同步完成`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
    await loadChannels()
  } finally {
    busy.value = false
    syncingID.value = 0
  }
}

async function syncAll() {
  busy.value = true
  try {
    const result = await api.syncAllUpstreamChannels()
    await loadChannels()
    if (!isSummaryPage.value && selectedID.value) await loadSelected()
    ui.notify(result.failed ? 'warning' : 'success', `已同步 ${result.synced} 个渠道${result.failed ? `，${result.failed} 个失败` : ''}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

function openChannelEditor(channel?: UpstreamChannel) {
  channelEditor.open = true
  channelEditor.id = channel?.id ?? 0
  channelEditor.name = channel?.name ?? ''
  channelEditor.type = channel?.type ?? 'sub2api'
  channelEditor.baseURL = channel?.base_url ?? ''
  channelEditor.username = channel?.username ?? ''
  channelEditor.password = channel?.password ?? ''
  channelEditor.newAPIAccessToken = channel?.newapi_access_token ?? ''
  channelEditor.newAPIUserID = channel?.newapi_user_id ?? ''
  channelEditor.rechargeRatio = channel?.recharge_ratio || 1
  channelEditor.rechargeMethods = [...(channel?.recharge_methods ?? [])]
  channelEditor.rechargeFee = channel?.recharge_fee ?? ''
  channelEditor.ignored = channel?.ignored ?? false
  channelEditor.sync = true
}

async function saveChannel() {
  busy.value = true
  const payload: Record<string, unknown> = {
    name: channelEditor.name,
    type: channelEditor.type,
    base_url: channelEditor.baseURL,
    recharge_ratio: channelEditor.rechargeRatio,
    recharge_methods: channelEditor.rechargeMethods,
    recharge_fee: channelEditor.rechargeFee,
    ignored: channelEditor.ignored
  }
  if (channelEditor.type === 'newapi') {
    payload.newapi_access_token = channelEditor.newAPIAccessToken
    payload.newapi_user_id = channelEditor.newAPIUserID
  } else {
    payload.username = channelEditor.username
    payload.password = channelEditor.password
  }
  if (channelEditor.id) payload.sync = channelEditor.sync
  try {
    const saved = channelEditor.id ? await api.updateUpstreamChannel(channelEditor.id, payload) : await api.createUpstreamChannel(payload)
    channelEditor.open = false
    await loadChannels()
    if (!isSummaryPage.value) {
      await router.replace({ path: '/upstream-channels/list', query: { id: String(saved.id), tab: 'overview' } })
      await loadSelected()
    }
    ui.notify('success', `渠道「${saved.name}」已保存`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

function toggleRechargeMethod(method: UpstreamRechargeMethod) {
  channelEditor.rechargeMethods = channelEditor.rechargeMethods.includes(method)
    ? channelEditor.rechargeMethods.filter(item => item !== method)
    : [...channelEditor.rechargeMethods, method]
}

async function setIgnored(channel: UpstreamChannel, ignored: boolean) {
  try {
    await api.updateUpstreamChannel(channel.id, { ignored })
    await loadChannels()
    if (selectedID.value === channel.id) await loadSelected()
    ui.notify('success', `「${channel.name}」${ignored ? '已忽略' : '已恢复'}`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function deleteChannel(channel: UpstreamChannel) {
  if (!window.confirm(`确定删除「${channel.name}」及其余额、任务和告警历史吗？`)) return
  try {
    await api.deleteUpstreamChannel(channel.id)
    if (selectedID.value === channel.id) await goBack()
    await loadChannels()
    ui.notify('success', `「${channel.name}」已删除`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

function openUpstream(channel: UpstreamChannel) {
  const target = channel.type === 'sub2api' ? `/api/upstream-channels/${channel.id}/login` : channel.base_url
  window.open(target, '_blank', 'noopener,noreferrer')
}

async function copyText(value: string) {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    ui.notify('success', '已复制')
  } catch {
    ui.notify('error', '复制失败')
  }
}

async function openEmailEditor() {
  try {
    const settings = await api.upstreamEmailSettings()
    emailEditor.open = true
    emailEditor.host = settings.smtp_host
    emailEditor.port = settings.smtp_port
    emailEditor.secure = settings.smtp_secure
    emailEditor.user = settings.smtp_user
    emailEditor.password = ''
    emailEditor.from = settings.smtp_from
    emailEditor.subjectPrefix = settings.subject_prefix
    emailEditor.recipients = settings.default_recipients.join(', ')
    emailEditor.testRecipient = ''
    emailEditor.interval = settings.default_interval_minutes
    emailEditor.hasPassword = settings.has_smtp_password
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

function emailPayload(): Record<string, unknown> {
  return { smtp_host: emailEditor.host, smtp_port: emailEditor.port, smtp_secure: emailEditor.secure, smtp_user: emailEditor.user, smtp_password: emailEditor.password, smtp_from: emailEditor.from, subject_prefix: emailEditor.subjectPrefix, default_recipients: splitEmails(emailEditor.recipients), default_interval_minutes: emailEditor.interval }
}

async function saveEmail() {
  busy.value = true
  try {
    await api.saveUpstreamEmailSettings(emailPayload())
    emailEditor.open = false
    ui.notify('success', '邮件设置已保存')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

async function testEmail() {
  busy.value = true
  try {
    await api.saveUpstreamEmailSettings(emailPayload())
    await api.testUpstreamEmailSettings(splitEmails(emailEditor.testRecipient || emailEditor.recipients))
    emailEditor.hasPassword = emailEditor.hasPassword || Boolean(emailEditor.password)
    emailEditor.password = ''
    ui.notify('success', '测试邮件已发送')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

async function openWeComEditor() {
  try {
    const settings = await api.upstreamWeComSettings()
    wecomEditor.open = true
    wecomEditor.corpID = settings.corp_id
    wecomEditor.agentID = settings.agent_id > 0 ? String(settings.agent_id) : ''
    wecomEditor.target = settings.target
    wecomEditor.secret = settings.secret
    wecomEditor.hasSecret = Boolean(settings.secret)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

function wecomPayload(): Record<string, unknown> {
  return {
    corp_id: wecomEditor.corpID,
    agent_id: parseWeComAgentID(),
    secret: wecomEditor.secret,
    target: wecomEditor.target
  }
}

function parseWeComAgentID(): number {
  const raw = wecomEditor.agentID.trim()
  if (!/^[1-9]\d*$/.test(raw)) {
    throw new Error('应用 AgentId 必须是纯数字；以 ww 开头的值是 CorpID，请填到上方企业 ID')
  }
  const value = Number(raw)
  if (!Number.isSafeInteger(value)) throw new Error('应用 AgentId 超出可用范围')
  return value
}

async function saveWeCom() {
  busy.value = true
  try {
    const saved = await api.saveUpstreamWeComSettings(wecomPayload())
    wecomEditor.secret = saved.secret
    wecomEditor.hasSecret = Boolean(saved.secret)
    wecomEditor.open = false
    ui.notify('success', '企微通知设置已保存')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

async function testWeCom() {
  busy.value = true
  try {
    const saved = await api.saveUpstreamWeComSettings(wecomPayload())
    wecomEditor.secret = saved.secret
    wecomEditor.hasSecret = Boolean(saved.secret)
    await api.testUpstreamWeComSettings(wecomEditor.target)
    ui.notify('success', '企微测试消息已发送')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

function openTaskEditor(task?: UpstreamAutomationTask) {
  taskEditor.open = true
  taskEditor.id = task?.id ?? 0
  taskEditor.type = task?.type ?? 'low_balance'
  taskEditor.enabled = task?.enabled ?? true
  taskEditor.interval = task?.interval_minutes ?? 5
  taskEditor.threshold = task?.threshold ?? 10
  taskEditor.lookback = task?.lookback_minutes ?? 60
  taskEditor.cooldown = task?.cooldown_minutes ?? 30
  taskEditor.recipients = task?.recipients.join(', ') ?? ''
}

function selectTaskType(type: UpstreamTaskType) {
  taskEditor.id = 0
  taskEditor.type = type
  taskEditor.interval = type === 'low_balance' ? 5 : 30
  taskEditor.cooldown = type === 'low_balance' ? 30 : 60
  taskEditor.lookback = 60
}

function isGroupTask(type: UpstreamTaskType) {
  return type.startsWith('group_')
}

function taskHint(type: UpstreamTaskType) {
  if (isGroupTask(type)) return '创建时记录当前分组作为基线；首次检查只建立基线，不发送告警。'
  if (type === 'burn_rate') return '按统计窗口内余额变化折算每小时消耗速度；充值或余额上涨不会触发。'
  return '只判断最新余额快照是否低于或等于阈值。'
}

async function createTaskInline() {
  taskEditor.id = 0
  taskEditor.enabled = true
  await saveTask()
}

async function saveTask() {
  if (!selectedChannel.value) return
  busy.value = true
  tabError.value = ''
  const payload: Record<string, unknown> = { type: taskEditor.type, enabled: taskEditor.enabled, interval_minutes: taskEditor.interval, lookback_minutes: taskEditor.lookback, cooldown_minutes: taskEditor.cooldown, recipients: splitEmails(taskEditor.recipients) }
  if (!isGroupTask(taskEditor.type)) payload.threshold = taskEditor.threshold
  try {
    if (taskEditor.id) await api.updateUpstreamTask(selectedChannel.value.id, taskEditor.id, payload)
    else await api.createUpstreamTask(selectedChannel.value.id, payload)
    taskEditor.open = false
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
    taskEditor.recipients = ''
    ui.notify('success', '自动任务已保存')
  } catch (err) {
    tabError.value = (err as Error).message
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

async function toggleTask(task: UpstreamAutomationTask, enabled: boolean) {
  if (!selectedChannel.value) return
  tabError.value = ''
  try {
    await api.updateUpstreamTask(selectedChannel.value.id, task.id, { enabled })
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
  } catch (err) {
    tabError.value = (err as Error).message
    ui.notify('error', (err as Error).message)
  }
}

async function deleteTask(task: UpstreamAutomationTask) {
  tabError.value = ''
  if (!selectedChannel.value || !window.confirm(`确定删除“${taskLabel(task.type)}”任务吗？`)) return
  try {
    await api.deleteUpstreamTask(selectedChannel.value.id, task.id)
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
    ui.notify('success', '自动任务已删除')
  } catch (err) {
    tabError.value = (err as Error).message
    ui.notify('error', (err as Error).message)
  }
}

async function showTokenModels(token: Row) {
  if (!selectedChannel.value) return
  const id = tokenID(token)
  if (!id) return ui.notify('error', '令牌 ID 无效')
  modelsModal.open = true
  modelsModal.loading = true
  modelsModal.id = id
  modelsModal.name = displayValue(token, ['name', 'title'], '')
  modelsModal.models = []
  try {
    modelsModal.models = (await api.upstreamTokenModels(selectedChannel.value.id, id)).models
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    modelsModal.loading = false
  }
}

function channelTypeLabel(type: UpstreamChannelType) { return type === 'sub2api' ? 'Sub2API' : type === 'newapi' ? 'New API' : '其它' }
function channelTypeTone(type: UpstreamChannelType) { return type === 'sub2api' ? 'primary' : type === 'newapi' ? 'purple' : 'gray' }
function channelStatusMeta(channel: UpstreamChannel) { return channel.status === 'active' ? { label: '正常', tone: 'success' } : channel.status === 'syncing' ? { label: '同步中', tone: 'primary' } : { label: '异常', tone: 'danger' } }
function channelStatusClass(channel: UpstreamChannel) { return channel.status === 'active' ? 'bg-emerald-500' : channel.status === 'syncing' ? 'bg-amber-500' : 'bg-red-500' }
function rechargeMethodLabel(method: UpstreamRechargeMethod) {
  return ({ alipay: '支付宝', wechat: '微信', card: '卡网' } as Record<UpstreamRechargeMethod, string>)[method] || method
}
function rechargeRatioLabel(channel: UpstreamChannel) {
  const ratio = channel.recharge_ratio > 0 ? channel.recharge_ratio : 1
  return `1 : ${formatNumber(ratio)}`
}
function groupName(group: Row) {
  const value = firstValue(group, ['name', 'display_name', 'group_name', 'title', 'key', 'id', 'group_id'])
  return value === undefined ? '' : String(value)
}

function updateChartHover(event: PointerEvent) {
  if (!chartCoordinates.value.length) return
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  const position = Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width))
  chartHoverIndex.value = Math.round(position * (chartCoordinates.value.length - 1))
}
function tokenColumnLabel(column: string) {
  return ({ id: 'ID', name: '名称', key: '密钥', status: '状态', remain_quota: '剩余额度', used_quota: '已用额度', expired_time: '过期时间', expires_at: '过期时间' } as Record<string, string>)[column] || column
}
function tokenPreview(value: unknown) {
  if (value === null || value === undefined || value === '') return '—'
  return String(value)
}
function tokenValue(token: Row, column: string, index: number) {
  if (column === 'name') return tokenPreview(firstValue(token, ['name', 'title']) ?? `令牌 ${index + 1}`)
  if (column === 'id') return tokenPreview(firstValue(token, ['id', 'ID']))
  if (column === 'status') return tableStatusLabel(tokenPreview(token[column]))
  if (column === 'expired_time' || column === 'expires_at') {
    const value = token[column]
    return typeof value === 'string' ? formatTime(value) : tokenPreview(value)
  }
  if (column === 'remain_quota') return tokenPreview(firstValue(token, ['remain_quota', 'quota', 'balance']))
  if (column === 'used_quota') return tokenPreview(firstValue(token, ['used_quota', 'quota_used']))
  return tokenPreview(token[column])
}
function tableStatusLabel(value: string) {
  return ({ active: '启用', inactive: '停用', disabled: '禁用', expired: '已过期', quota_exhausted: '额度耗尽', exhausted: '已耗尽' } as Record<string, string>)[value.toLowerCase()] || value
}
function credentialLabel(channel: UpstreamChannel) {
  if (channel.type === 'newapi') return channel.newapi_user_id ? `系统令牌 · 用户 ${channel.newapi_user_id}` : '系统令牌'
  return channel.username ? `账号 ${channel.username}` : '未配置账号'
}
function summaryBalance(channel: UpstreamChannel) {
  const snapshot = summaryOverviews.value[channel.id]?.latest_snapshot
  if (snapshot) return formatNumber(snapshot.balance)
  return channel.latest_balance == null ? '—' : formatNumber(channel.latest_balance)
}
function summaryTokenRows(channel: UpstreamChannel) { return toRows(summaryOverviews.value[channel.id]?.tokens) }
function tokenGroupValue(token: Row, channelType = selectedChannel.value?.type) {
  if (channelType === 'newapi') {
    const group = firstValue(token, ['group', 'Group'])
    if (isRow(group)) return groupName(group)
    return group === undefined ? '' : String(group)
  }
  const direct = firstValue(token, ['group_id', 'groupId', 'groupID'])
  if (direct !== undefined) return String(direct)
  const group = firstValue(token, ['group', 'Group', 'group_name', 'groupName'])
  if (isRow(group)) return String(firstValue(group, ['id', 'ID', 'name', 'key', 'group_id', 'group_name']) ?? '')
  return group === undefined ? '' : String(group)
}
function tokenOptions(token: Row) {
  const options = [...groupOptions.value]
  const current = tokenGroupValue(token)
  if (current && !options.some(option => option.value === current)) options.push({ value: current, label: tokenGroupLabel(token) })
  return options
}
async function changeTokenGroup(token: Row, event: Event) {
  const target = event.target as HTMLSelectElement | null
  const nextValue = target?.value || ''
  if (!selectedChannel.value || !tokenID(token) || (selectedChannel.value.type === 'sub2api' && !nextValue)) return
  busy.value = true
  try {
    const payload = selectedChannel.value.type === 'sub2api' ? { group_id: Number(nextValue) } : { group: nextValue }
    await api.updateUpstreamTokenGroup(selectedChannel.value.id, tokenID(token), payload)
    overview.value = await api.upstreamOverview(selectedChannel.value.id)
    ui.notify('success', '令牌分组已更新')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}
function tokenRatioLabel(channel: UpstreamChannel, token: Row) {
  const group = tokenGroup(channel, token)
  const embedded = isRow(token.group) ? token.group : undefined
  const value = firstNumeric(group, ['user_rate_multiplier', 'userRateMultiplier', 'custom_rate_multiplier', 'customRateMultiplier'])
    ?? firstNumeric(embedded, ['user_rate_multiplier', 'rate_multiplier', 'ratio', 'rate', 'multiplier', 'value'])
    ?? firstNumeric(group, ['rate_multiplier', 'ratio', 'rate', 'multiplier', 'value'])
  return value === null ? '—' : `${formatNumber(value)}x`
}
function tokenRatioChange(channel: UpstreamChannel, token: Row) {
  const overviewData = summaryOverviews.value[channel.id] ?? (selectedChannel.value?.id === channel.id ? overview.value : undefined)
  const identifiers = [...new Set([
    ...rowIdentifiers(tokenGroup(channel, token), ['id', 'group_id', 'name', 'key', 'code', 'group_name', 'display_name']),
    ...rowIdentifiers(token, ['group_id', 'groupId', 'groupID', 'group_name', 'groupName', 'group'])
  ])]
  return overviewData?.recent_group_ratio_changes?.find(change =>
    identifiers.includes(change.key.trim().toLowerCase()) || identifiers.includes(change.label.trim().toLowerCase()))
}
function tokenGroup(channel: UpstreamChannel, token: Row) {
  const overviewData = summaryOverviews.value[channel.id] ?? (selectedChannel.value?.id === channel.id ? overview.value : undefined)
  const tokenIDs = rowIdentifiers(token, ['group_id', 'groupId', 'groupID', 'group_name', 'groupName', 'group'])
  return toRows(overviewData?.groups).find(item => tokenIDs.some(id => rowIdentifiers(item, ['id', 'group_id', 'name', 'key', 'code', 'group_name', 'display_name']).includes(id)))
}
function rowIdentifiers(row: Row | undefined, keys: string[]) {
  if (!row) return []
  const values: string[] = []
  for (const key of keys) {
    const value = row[key]
    if (isRow(value)) values.push(...rowIdentifiers(value, ['id', 'group_id', 'name', 'key', 'code', 'group_name', 'display_name']))
    else if (value !== undefined && value !== null && String(value).trim()) values.push(String(value).trim().toLowerCase())
  }
  return [...new Set(values)]
}
function firstNumeric(row: Row | undefined, keys: string[]) {
  if (!row) return null
  for (const key of keys) {
    const raw = row[key]
    if (typeof raw === 'string' && !raw.trim()) continue
    const value = typeof raw === 'number' ? raw : typeof raw === 'string' ? Number(raw) : NaN
    if (typeof value === 'number' && Number.isFinite(value)) return value
  }
  return null
}
function taskLabel(type: UpstreamTaskType) { return ({ low_balance: '低余额', burn_rate: '消耗速率', group_added: '新增分组', group_removed: '移除分组', group_ratio_changed: '分组倍率变化' } as Record<UpstreamTaskType, string>)[type] }
function taskTone(type: UpstreamTaskType) { return type === 'low_balance' || type === 'burn_rate' ? 'warning' : 'primary' }
function formatNumber(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return '—'
  return Number.isInteger(value) ? String(value) : value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
}
function splitEmails(value: string) { return value.split(/[,;\n\r]+/).map(item => item.trim()).filter(Boolean) }
function toRows(value: unknown): Row[] {
  if (Array.isArray(value)) return value.map((item, index) => isRow(item) ? item : { value: item, index })
  if (isRow(value)) return Object.entries(value).map(([name, item]) => isRow(item) ? { name, ...item } : { name, value: item })
  return []
}
function isRow(value: unknown): value is Row { return Boolean(value && typeof value === 'object' && !Array.isArray(value)) }
function firstValue(row: Row, keys: string[]) { for (const key of keys) if (row[key] !== undefined && row[key] !== null && row[key] !== '') return row[key]; return undefined }
function displayValue(row: Row, keys: string[], fallback: string) { const value = firstValue(row, keys); return value === undefined ? fallback : typeof value === 'object' ? JSON.stringify(value) : String(value) }
function rowKey(row: Row, index: number) { return String(firstValue(row, ['id', 'ID', 'key', 'name']) ?? index) }
function tokenID(token: Row) { const value = Number(firstValue(token, ['id', 'ID'])); return Number.isInteger(value) && value > 0 ? value : 0 }
function tokenGroupLabel(token: Row) { return tokenGroupValue(token) || '默认' }
</script>
