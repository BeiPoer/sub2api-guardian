<template>
  <AppLayout title="上游渠道" subtitle="外部渠道连接、余额、令牌与自动告警">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div v-if="selectedChannel" class="flex min-w-0 items-center gap-2">
        <button type="button" class="btn btn-ghost btn-icon" title="返回渠道列表" @click="goBack">
          <Icon name="arrowLeft" size="sm" />
        </button>
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="truncate text-lg font-semibold text-gray-900 dark:text-white">
              {{ selectedChannel.name }}
            </h2>
            <Badge :tone="channelStatusMeta(selectedChannel).tone" dot>
              {{ channelStatusMeta(selectedChannel).label }}
            </Badge>
            <Badge v-if="selectedChannel.ignored" tone="gray">已忽略</Badge>
          </div>
          <p class="truncate text-xs text-gray-500 dark:text-dark-400">{{ selectedChannel.base_url }}</p>
        </div>
      </div>
      <div v-else class="min-w-0">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">渠道目录</h2>
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ channels.length }} 个上游渠道</p>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <template v-if="selectedChannel">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="openChannelEditor(selectedChannel)">
            <Icon name="edit" size="sm" />
            编辑
          </button>
          <button
            v-if="selectedChannel.type !== 'other'"
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="busy || selectedChannel.ignored"
            @click="syncChannel(selectedChannel)"
          >
            <Icon name="refresh" size="sm" />
            同步
          </button>
          <button type="button" class="btn btn-secondary btn-sm" @click="openUpstream(selectedChannel)">
            <Icon name="externalLink" size="sm" />
            进入上游
          </button>
        </template>
        <button type="button" class="btn btn-secondary btn-sm" @click="openEmailEditor">
          <Icon name="mail" size="sm" />
          邮件
        </button>
        <button v-if="!selectedChannel" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="syncAll">
          <Icon name="refresh" size="sm" />
          全部同步
        </button>
        <button v-if="!selectedChannel" type="button" class="btn btn-primary btn-sm" @click="openChannelEditor()">
          <Icon name="plus" size="sm" />
          新增渠道
        </button>
      </div>
    </div>

    <template v-if="!selectedChannel">
      <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary"><Icon name="server" size="lg" /></div>
          <div class="min-w-0"><p class="stat-value">{{ channels.length }}</p><p class="stat-label">渠道总数</p></div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-success"><Icon name="checkCircle" size="lg" /></div>
          <div class="min-w-0"><p class="stat-value">{{ activeCount }}</p><p class="stat-label">未忽略</p></div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-warning"><Icon name="dollar" size="lg" /></div>
          <div class="min-w-0"><p class="stat-value text-base">{{ balanceSummary }}</p><p class="stat-label">余额汇总</p></div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary"><Icon name="key" size="lg" /></div>
          <div class="min-w-0"><p class="stat-value">{{ tokenTotal }}</p><p class="stat-label">令牌总数</p></div>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="tabs">
          <button type="button" class="tab" :class="viewMode === 'active' && 'tab-active'" @click="viewMode = 'active'">
            未忽略 {{ activeCount }}
          </button>
          <button type="button" class="tab" :class="viewMode === 'ignored' && 'tab-active'" @click="viewMode = 'ignored'">
            已忽略 {{ ignoredCount }}
          </button>
        </div>
        <label class="relative w-full sm:w-64">
          <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-2.5 text-gray-400" />
          <input v-model="search" class="input pl-9" placeholder="搜索名称、类型或地址" />
        </label>
      </div>

      <div v-if="filteredChannels.length" class="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <article v-for="channel in filteredChannels" :key="channel.id" class="card card-hover">
          <button type="button" class="block w-full p-5 text-left" @click="selectChannel(channel)">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ channel.name }}</h3>
                  <Badge :tone="channelTypeTone(channel.type)">{{ channelTypeLabel(channel.type) }}</Badge>
                  <Badge :tone="channelStatusMeta(channel).tone" dot>{{ channelStatusMeta(channel).label }}</Badge>
                </div>
                <p class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400">{{ channel.base_url }}</p>
              </div>
              <Icon name="chevronRight" size="sm" class="mt-1 flex-shrink-0 text-gray-400" />
            </div>
            <div class="mt-4 grid grid-cols-3 gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
              <div><p class="text-xs text-gray-500 dark:text-dark-400">余额</p><p class="mt-1 truncate text-sm font-medium text-gray-900 dark:text-white">{{ channelBalance(channel) }}</p></div>
              <div><p class="text-xs text-gray-500 dark:text-dark-400">令牌</p><p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ channel.token_count ?? 0 }}</p></div>
              <div><p class="text-xs text-gray-500 dark:text-dark-400">上次同步</p><p class="mt-1 truncate text-sm font-medium text-gray-900 dark:text-white">{{ formatRelative(channel.last_sync_at) }}</p></div>
            </div>
            <p v-if="channel.last_error" class="mt-3 line-clamp-2 text-xs text-red-600 dark:text-red-400">{{ channel.last_error }}</p>
          </button>
          <div class="flex items-center justify-end gap-1 border-t border-gray-100 px-4 py-2 dark:border-dark-700">
            <button
              v-if="channel.type !== 'other'"
              type="button"
              class="btn btn-ghost btn-icon"
              title="同步渠道"
              :disabled="busy || channel.ignored"
              @click="syncChannel(channel)"
            ><Icon name="refresh" size="sm" /></button>
            <button type="button" class="btn btn-ghost btn-icon" title="进入上游" @click="openUpstream(channel)"><Icon name="externalLink" size="sm" /></button>
            <button type="button" class="btn btn-ghost btn-icon" title="编辑渠道" @click="openChannelEditor(channel)"><Icon name="edit" size="sm" /></button>
            <button type="button" class="btn btn-ghost btn-icon" :title="channel.ignored ? '取消忽略' : '忽略渠道'" @click="setIgnored(channel, !channel.ignored)">
              <Icon :name="channel.ignored ? 'play' : 'pause'" size="sm" />
            </button>
            <button type="button" class="btn btn-ghost btn-icon text-red-600" title="删除渠道" @click="deleteChannel(channel)"><Icon name="trash" size="sm" /></button>
          </div>
        </article>
      </div>
      <div v-else class="card">
        <EmptyState icon="server" title="没有匹配的上游渠道" description="新增渠道或调整当前筛选。" />
      </div>
    </template>

    <template v-else>
      <div class="overflow-x-auto">
        <div class="tabs min-w-max">
          <button v-for="tab in detailTabs" :key="tab.value" type="button" class="tab" :class="currentTab === tab.value && 'tab-active'" @click="setTab(tab.value)">
            {{ tab.label }}
          </button>
        </div>
      </div>

      <template v-if="currentTab === 'overview'">
        <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <div class="stat-card"><div class="stat-icon stat-icon-success"><Icon name="dollar" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">{{ selectedBalance }}</p><p class="stat-label">当前余额</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="key" size="lg" /></div><div class="min-w-0"><p class="stat-value">{{ tokenRows.length }}</p><p class="stat-label">令牌</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-primary"><Icon name="grid" size="lg" /></div><div class="min-w-0"><p class="stat-value">{{ groupRows.length }}</p><p class="stat-label">分组</p></div></div>
          <div class="stat-card"><div class="stat-icon stat-icon-warning"><Icon name="clock" size="lg" /></div><div class="min-w-0"><p class="stat-value text-base">{{ formatRelative(selectedChannel.last_sync_at) }}</p><p class="stat-label">上次同步</p></div></div>
        </div>

        <section class="card">
          <div class="card-header flex flex-wrap items-center justify-between gap-2">
            <div><h3 class="font-semibold text-gray-900 dark:text-white">连接凭据</h3><p class="text-xs text-gray-500 dark:text-dark-400">{{ selectedChannel.base_url }}</p></div>
            <Badge :tone="channelTypeTone(selectedChannel.type)">{{ channelTypeLabel(selectedChannel.type) }}</Badge>
          </div>
          <div class="divide-y divide-gray-100 px-6 dark:divide-dark-700">
            <div v-if="selectedChannel.type !== 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[9rem_1fr_auto] sm:items-center">
              <span class="text-sm text-gray-500 dark:text-dark-400">账号</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.username || '—' }}</code>
              <button type="button" class="btn btn-ghost btn-icon" title="复制账号" @click="copyText(selectedChannel.username)"><Icon name="copy" size="sm" /></button>
            </div>
            <div v-if="selectedChannel.type !== 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[9rem_1fr_auto] sm:items-center">
              <span class="text-sm text-gray-500 dark:text-dark-400">密码</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.password || '—' }}</code>
              <button type="button" class="btn btn-ghost btn-icon" title="复制密码" @click="copyText(selectedChannel.password)"><Icon name="copy" size="sm" /></button>
            </div>
            <div v-if="selectedChannel.type === 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[9rem_1fr_auto] sm:items-center">
              <span class="text-sm text-gray-500 dark:text-dark-400">用户 ID</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.newapi_user_id }}</code>
              <button type="button" class="btn btn-ghost btn-icon" title="复制用户 ID" @click="copyText(selectedChannel.newapi_user_id)"><Icon name="copy" size="sm" /></button>
            </div>
            <div v-if="selectedChannel.type === 'newapi'" class="grid gap-2 py-4 sm:grid-cols-[9rem_1fr_auto] sm:items-center">
              <span class="text-sm text-gray-500 dark:text-dark-400">系统令牌</span><code class="min-w-0 break-all text-sm text-gray-900 dark:text-white">{{ selectedChannel.newapi_access_token }}</code>
              <button type="button" class="btn btn-ghost btn-icon" title="复制系统令牌" @click="copyText(selectedChannel.newapi_access_token)"><Icon name="copy" size="sm" /></button>
            </div>
          </div>
        </section>

        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <section class="card">
            <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">余额趋势</h3></div>
            <div class="card-body">
              <svg v-if="chartPoints" viewBox="0 0 600 180" class="aspect-[10/3] w-full" role="img" aria-label="最近余额趋势">
                <line x1="20" y1="150" x2="580" y2="150" class="stroke-gray-200 dark:stroke-dark-600" />
                <polyline :points="chartPoints" fill="none" class="stroke-primary-500" stroke-width="3" stroke-linejoin="round" stroke-linecap="round" />
              </svg>
              <EmptyState v-else icon="chart" title="暂无余额历史" description="同步渠道后会记录余额快照。" />
            </div>
          </section>
          <section class="card">
            <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">账户资料</h3></div>
            <div class="card-body"><pre class="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs text-gray-700 dark:text-dark-300">{{ prettyJSON(overview?.profile) }}</pre></div>
          </section>
        </div>

        <section v-if="selectedChannel.type !== 'other'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">分组</h3></div>
          <div class="table-container m-4">
            <table class="table">
              <thead><tr><th>名称</th><th>ID / 标识</th><th>倍率</th><th>平台</th></tr></thead>
              <tbody><tr v-for="(group, index) in groupRows" :key="rowKey(group, index)"><td class="font-medium">{{ displayValue(group, ['name', 'display_name', 'group_name', 'key'], `分组 ${index + 1}`) }}</td><td>{{ displayValue(group, ['id', 'group_id', 'code'], '—') }}</td><td>{{ displayValue(group, ['user_rate_multiplier', 'rate_multiplier', 'ratio', 'rate', 'value'], '—') }}</td><td>{{ displayValue(group, ['platform'], '—') }}</td></tr></tbody>
            </table>
            <EmptyState v-if="!groupRows.length" icon="grid" title="暂无分组" description="上游没有返回可用分组。" />
          </div>
        </section>

        <section v-if="selectedChannel.type !== 'other'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">令牌</h3></div>
          <div class="table-container m-4">
            <table class="table">
              <thead><tr><th>名称</th><th>ID</th><th>分组</th><th>额度</th><th class="text-right">操作</th></tr></thead>
              <tbody>
                <tr v-for="(token, index) in tokenRows" :key="rowKey(token, index)">
                  <td class="font-medium">{{ displayValue(token, ['name', 'title'], `令牌 ${index + 1}`) }}</td><td>{{ displayValue(token, ['id', 'ID'], '—') }}</td><td>{{ tokenGroupLabel(token) }}</td><td>{{ displayValue(token, ['remain_quota', 'quota', 'balance'], '—') }}</td>
                  <td><div class="flex justify-end gap-1"><button type="button" class="btn btn-ghost btn-icon" title="查看模型" @click="showTokenModels(token)"><Icon name="cube" size="sm" /></button><button type="button" class="btn btn-ghost btn-icon" title="修改分组" @click="openTokenGroupEditor(token)"><Icon name="swap" size="sm" /></button></div></td>
                </tr>
              </tbody>
            </table>
            <EmptyState v-if="!tokenRows.length" icon="key" title="暂无令牌" description="上游没有返回令牌。" />
          </div>
        </section>

        <section v-if="selectedChannel.type === 'sub2api'" class="card">
          <div class="card-header"><h3 class="font-semibold text-gray-900 dark:text-white">订阅</h3></div>
          <div class="card-body"><pre class="max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs text-gray-700 dark:text-dark-300">{{ prettyJSON(overview?.subscriptions) }}</pre></div>
        </section>
      </template>

      <template v-else-if="currentTab === 'automation'">
        <div class="flex justify-end"><button type="button" class="btn btn-primary btn-sm" :disabled="selectedChannel.type === 'other'" @click="openTaskEditor()"><Icon name="plus" size="sm" />新增任务</button></div>
        <section class="card">
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
      </template>

      <template v-else-if="currentTab === 'balance-logs'">
        <section class="card">
          <div class="table-container m-4">
            <table class="table"><thead><tr><th>时间</th><th>状态</th><th>余额</th><th>已用</th><th>信息</th></tr></thead><tbody><tr v-for="item in balanceLogs" :key="item.id"><td class="whitespace-nowrap">{{ formatTime(item.created_at) }}</td><td><Badge :tone="item.status === 'success' ? 'success' : 'danger'">{{ item.status === 'success' ? '成功' : '失败' }}</Badge></td><td>{{ item.balance === undefined ? '—' : `${formatNumber(item.balance)} ${item.unit || ''}` }}</td><td>{{ item.used_balance === undefined ? '—' : formatNumber(item.used_balance) }}</td><td class="max-w-md break-words">{{ item.error || item.message }}</td></tr></tbody></table>
            <EmptyState v-if="!balanceLogs.length" icon="document" title="暂无余额查询日志" description="同步或自动检查后会生成日志。" />
          </div>
          <div class="px-6 pb-4"><MiniPager :page="logPage" :page-size="logPageSize" :total="logTotal" @update:page="changeLogPage" /></div>
        </section>
      </template>

      <template v-else>
        <section class="card">
          <div class="table-container m-4">
            <table class="table"><thead><tr><th>时间</th><th>类型</th><th>消息</th><th>邮件</th></tr></thead><tbody><tr v-for="alert in alerts" :key="alert.id"><td class="whitespace-nowrap">{{ formatTime(alert.created_at) }}</td><td><Badge :tone="taskTone(alert.type)">{{ taskLabel(alert.type) }}</Badge></td><td class="max-w-xl break-words">{{ alert.message }}</td><td><Badge :tone="alert.email_sent ? 'success' : 'danger'">{{ alert.email_sent ? '已发送' : alert.email_error || '未发送' }}</Badge></td></tr></tbody></table>
            <EmptyState v-if="!alerts.length" icon="bell" title="暂无告警事件" description="任务达到阈值后会记录事件。" />
          </div>
        </section>
      </template>
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
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="emailEditor.interval" label="默认检查间隔" type="number" :min="1" suffix="分钟" /><SwitchRow v-model="emailEditor.secure" label="隐式 TLS" description="通常用于 465 端口。" /></div>
      </div>
      <template #footer><button type="button" class="btn btn-secondary" :disabled="busy" @click="testEmail"><Icon name="mail" size="sm" />测试</button><button type="button" class="btn btn-secondary" @click="emailEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveEmail">保存</button></template>
    </Modal>

    <Modal :open="taskEditor.open" :title="taskEditor.id ? '编辑自动任务' : '新增自动任务'" @close="taskEditor.open = false">
      <div class="space-y-4">
        <label class="block"><span class="input-label">任务类型</span><select v-model="taskEditor.type" class="input"><option v-for="type in taskTypes" :key="type" :value="type">{{ taskLabel(type) }}</option></select></label>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><Field v-model="taskEditor.interval" label="检查间隔" type="number" :min="1" suffix="分钟" /><Field v-if="!taskEditor.type.startsWith('group_')" v-model="taskEditor.threshold" label="阈值" type="number" :step="0.01" /><Field v-if="taskEditor.type === 'burn_rate'" v-model="taskEditor.lookback" label="观察窗口" type="number" :min="1" suffix="分钟" /><Field v-model="taskEditor.cooldown" label="告警冷却" type="number" :min="0" suffix="分钟" /></div>
        <Field v-model="taskEditor.recipients" label="收件人" hint="留空使用默认收件人，多个邮箱使用逗号分隔" />
        <SwitchRow v-model="taskEditor.enabled" label="启用任务" />
      </div>
      <template #footer><button type="button" class="btn btn-secondary" @click="taskEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveTask">保存</button></template>
    </Modal>

    <Modal :open="modelsModal.open" :title="`令牌模型 · ${modelsModal.name || modelsModal.id}`" @close="modelsModal.open = false">
      <div v-if="modelsModal.loading" class="flex justify-center py-12"><span class="spinner text-primary-500" /></div>
      <div v-else class="flex flex-wrap gap-2"><Badge v-for="model in modelsModal.models" :key="model" tone="primary">{{ model }}</Badge><EmptyState v-if="!modelsModal.models.length" icon="cube" title="没有返回模型" description="令牌和上游模型接口均未返回可用模型。" /></div>
    </Modal>

    <Modal :open="groupEditor.open" :title="`修改令牌分组 · ${groupEditor.name || groupEditor.id}`" @close="groupEditor.open = false">
      <label v-if="selectedChannel?.type === 'sub2api'" class="block"><span class="input-label">分组</span><select v-model="groupEditor.value" class="input"><option value="">请选择</option><option v-for="option in groupOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select></label>
      <Field v-else v-model="groupEditor.value" label="分组标识" />
      <template #footer><button type="button" class="btn btn-secondary" @click="groupEditor.open = false">取消</button><button type="button" class="btn btn-primary" :disabled="busy" @click="saveTokenGroup">保存</button></template>
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
import MiniPager from '@/components/MiniPager.vue'
import Modal from '@/components/Modal.vue'
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
const search = ref('')
const viewMode = ref<'active' | 'ignored'>('active')
const logPage = ref(1)
const logPageSize = ref(20)
const logTotal = ref(0)

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
const activeCount = computed(() => channels.value.filter(item => !item.ignored).length)
const ignoredCount = computed(() => channels.value.filter(item => item.ignored).length)
const tokenTotal = computed(() => channels.value.reduce((sum, item) => sum + (item.token_count ?? 0), 0))
const balanceSummary = computed(() => {
  const totals = new Map<string, number>()
  for (const channel of channels.value) {
    if (channel.latest_balance == null) continue
    const unit = channel.balance_unit || '余额'
    totals.set(unit, (totals.get(unit) || 0) + channel.latest_balance)
  }
  return Array.from(totals.entries()).map(([unit, value]) => `${formatNumber(value)} ${unit}`).join(' / ') || '—'
})
const filteredChannels = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return channels.value.filter(channel => {
    if ((viewMode.value === 'ignored') !== channel.ignored) return false
    if (!needle) return true
    return [channel.name, channel.type, channel.base_url, channel.username, channel.newapi_user_id]
      .join(' ')
      .toLowerCase()
      .includes(needle)
  })
})
const groupRows = computed(() => toRows(overview.value?.groups))
const tokenRows = computed(() => toRows(overview.value?.tokens))
const selectedBalance = computed(() => {
  const snapshot = overview.value?.latest_snapshot
  return snapshot ? `${formatNumber(snapshot.balance)} ${snapshot.unit}` : '—'
})
const chartPoints = computed(() => {
  const history = overview.value?.history ?? []
  if (!history.length) return ''
  const values = history.map(item => item.balance)
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = max - min || 1
  return values.map((value, index) => {
    const x = history.length === 1 ? 300 : 20 + (index / (history.length - 1)) * 560
    const y = 150 - ((value - min) / span) * 120
    return `${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
})
const groupOptions = computed(() => groupRows.value.map((group, index) => ({
  value: String(firstValue(group, ['id', 'group_id', 'name', 'key']) ?? ''),
  label: displayValue(group, ['name', 'display_name', 'group_name', 'key'], `分组 ${index + 1}`)
})).filter(option => option.value))

const channelEditor = reactive({ open: false, id: 0, name: '', type: 'sub2api' as UpstreamChannelType, baseURL: '', username: '', password: '', newAPIAccessToken: '', newAPIUserID: '', ignored: false, sync: true })
const emailEditor = reactive({ open: false, host: '', port: 587, secure: false, user: '', password: '', from: '', subjectPrefix: '', recipients: '', interval: 30, hasPassword: false })
const taskTypes: UpstreamTaskType[] = ['low_balance', 'burn_rate', 'group_added', 'group_removed', 'group_ratio_changed']
const taskEditor = reactive({ open: false, id: 0, type: 'low_balance' as UpstreamTaskType, enabled: true, interval: 5, threshold: 10, lookback: 60, cooldown: 30, recipients: '' })
const modelsModal = reactive({ open: false, loading: false, id: 0, name: '', models: [] as string[] })
const groupEditor = reactive({ open: false, id: 0, name: '', value: '' })

onMounted(async () => {
  await loadChannels()
  await loadSelected()
})

watch(selectedID, async (next, previous) => {
  if (next !== previous) await loadSelected()
})

watch(currentTab, async () => {
  await loadTabData()
})

async function loadChannels() {
  try {
    const data = await api.upstreamChannels()
    channels.value = data.items
    if (selectedID.value && !channels.value.some(item => item.id === selectedID.value)) await goBack()
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function loadSelected() {
  overview.value = null
  tasks.value = []
  balanceLogs.value = []
  alerts.value = []
  if (!selectedID.value) return
  if (selectedChannel.value?.type === 'other' && route.query.tab !== 'overview') {
    await router.replace({ query: { id: String(selectedID.value), tab: 'overview' } })
  }
  try {
    overview.value = await api.upstreamOverview(selectedID.value)
    await loadTabData()
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function loadTabData() {
  if (!selectedID.value) return
  try {
    if (currentTab.value === 'automation') tasks.value = (await api.upstreamTasks(selectedID.value)).items
    if (currentTab.value === 'balance-logs') await loadBalanceLogs(logPage.value)
    if (currentTab.value === 'alerts') alerts.value = (await api.upstreamAlerts(selectedID.value)).items
  } catch (err) {
    ui.notify('error', (err as Error).message)
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
  await loadBalanceLogs(page)
}

function selectChannel(channel: UpstreamChannel) {
  void router.push({ path: '/upstream-channels', query: { id: String(channel.id), tab: 'overview' } })
}

async function goBack() {
  await router.push({ path: '/upstream-channels' })
}

function setTab(tab: DetailTab) {
  logPage.value = 1
  void router.replace({ query: { id: String(selectedID.value), tab } })
}

async function syncChannel(channel: UpstreamChannel) {
  busy.value = true
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
  }
}

async function syncAll() {
  busy.value = true
  try {
    const result = await api.syncAllUpstreamChannels()
    await loadChannels()
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
  channelEditor.ignored = channel?.ignored ?? false
  channelEditor.sync = true
}

async function saveChannel() {
  busy.value = true
  const payload: Record<string, unknown> = { name: channelEditor.name, type: channelEditor.type, base_url: channelEditor.baseURL, ignored: channelEditor.ignored }
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
    if (selectedID.value === saved.id) await loadSelected()
    ui.notify('success', `渠道「${saved.name}」已保存`)
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
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
    await api.testUpstreamEmailSettings(splitEmails(emailEditor.recipients))
    emailEditor.hasPassword = emailEditor.hasPassword || Boolean(emailEditor.password)
    emailEditor.password = ''
    ui.notify('success', '测试邮件已发送')
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

async function saveTask() {
  if (!selectedChannel.value) return
  busy.value = true
  const payload: Record<string, unknown> = { type: taskEditor.type, enabled: taskEditor.enabled, interval_minutes: taskEditor.interval, threshold: taskEditor.threshold, lookback_minutes: taskEditor.lookback, cooldown_minutes: taskEditor.cooldown, recipients: splitEmails(taskEditor.recipients) }
  try {
    if (taskEditor.id) await api.updateUpstreamTask(selectedChannel.value.id, taskEditor.id, payload)
    else await api.createUpstreamTask(selectedChannel.value.id, payload)
    taskEditor.open = false
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
    ui.notify('success', '自动任务已保存')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

async function toggleTask(task: UpstreamAutomationTask, enabled: boolean) {
  if (!selectedChannel.value) return
  try {
    await api.updateUpstreamTask(selectedChannel.value.id, task.id, { enabled })
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function deleteTask(task: UpstreamAutomationTask) {
  if (!selectedChannel.value || !window.confirm(`确定删除“${taskLabel(task.type)}”任务吗？`)) return
  try {
    await api.deleteUpstreamTask(selectedChannel.value.id, task.id)
    tasks.value = (await api.upstreamTasks(selectedChannel.value.id)).items
    ui.notify('success', '自动任务已删除')
  } catch (err) {
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

function openTokenGroupEditor(token: Row) {
  const id = tokenID(token)
  if (!id) return ui.notify('error', '令牌 ID 无效')
  groupEditor.open = true
  groupEditor.id = id
  groupEditor.name = displayValue(token, ['name', 'title'], '')
  groupEditor.value = String(firstValue(token, ['group_id', 'group', 'group_name']) ?? '')
}

async function saveTokenGroup() {
  if (!selectedChannel.value) return
  busy.value = true
  try {
    const payload = selectedChannel.value.type === 'sub2api' ? { group_id: Number(groupEditor.value) } : { group: groupEditor.value }
    await api.updateUpstreamTokenGroup(selectedChannel.value.id, groupEditor.id, payload)
    groupEditor.open = false
    overview.value = await api.upstreamOverview(selectedChannel.value.id)
    ui.notify('success', '令牌分组已更新')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    busy.value = false
  }
}

function channelTypeLabel(type: UpstreamChannelType) { return type === 'sub2api' ? 'Sub2API' : type === 'newapi' ? 'New API' : '其它' }
function channelTypeTone(type: UpstreamChannelType) { return type === 'sub2api' ? 'primary' : type === 'newapi' ? 'purple' : 'gray' }
function channelStatusMeta(channel: UpstreamChannel) { return channel.status === 'active' ? { label: '正常', tone: 'success' } : channel.status === 'syncing' ? { label: '同步中', tone: 'primary' } : { label: '异常', tone: 'danger' } }
function channelBalance(channel: UpstreamChannel) { return channel.latest_balance == null ? '—' : `${formatNumber(channel.latest_balance)} ${channel.balance_unit || ''}` }
function taskLabel(type: UpstreamTaskType) { return ({ low_balance: '低余额', burn_rate: '消耗速率', group_added: '新增分组', group_removed: '移除分组', group_ratio_changed: '分组倍率变化' } as Record<UpstreamTaskType, string>)[type] }
function taskTone(type: UpstreamTaskType) { return type === 'low_balance' || type === 'burn_rate' ? 'warning' : 'primary' }
function formatNumber(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return '—'
  return Number.isInteger(value) ? String(value) : value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
}
function splitEmails(value: string) { return value.split(/[,;\n\r]+/).map(item => item.trim()).filter(Boolean) }
function prettyJSON(value: unknown) { return value === undefined || value === null ? '—' : JSON.stringify(value, null, 2) }
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
function tokenGroupLabel(token: Row) { return displayValue(token, ['group_name', 'group', 'group_id'], '默认') }
</script>
