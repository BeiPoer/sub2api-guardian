<template>
  <AppLayout title="连接设置" subtitle="配置 sub2api 管理端地址与 Admin API Key，并控制自动守护开关">
    <div class="grid grid-cols-1 gap-6 xl:grid-cols-3">
      <section class="card xl:col-span-2">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">sub2api 连接</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            Admin API Key 可在 sub2api 后台「系统设置 → 管理端 API Key」获取；保存后不会回显。
          </p>
        </div>
        <div class="space-y-4 p-6">
          <Field v-model="form.baseURL" label="sub2api 地址" placeholder="http://127.0.0.1:8080" />
          <Field
            v-model="form.adminKey"
            label="Admin API Key"
            type="password"
            :placeholder="hasKey ? '已配置，留空则不修改' : '必填'"
            :hint="hasKey ? '已保存一个 Key；留空提交表示保持不变。' : '尚未配置，守护流程无法启动。'"
          />
          <Field
            v-model="form.timeout"
            label="请求超时"
            suffix="秒"
            type="number"
            :min="5"
            hint="探测走 SSE 流，建议不低于 30 秒"
          />
          <SwitchRow
            v-model="form.enabled"
            label="自动守护"
            description="关闭后引擎停止后台调度，仍可手动同步与探测"
          />

          <div class="flex flex-wrap items-center gap-2 pt-2">
            <button type="button" class="btn btn-primary" :disabled="guardian.busy" @click="save">
              <Icon name="check" size="sm" />
              保存连接
            </button>
            <button type="button" class="btn btn-secondary" :disabled="guardian.busy" @click="testConnection">
              <Icon name="link" size="sm" />
              保存并测试同步
            </button>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">运行状态</h2>
        </div>
        <dl class="space-y-3 p-6 text-sm">
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">连接配置</dt>
            <dd>
              <Badge :tone="guardian.configured ? 'success' : 'danger'" dot>
                {{ guardian.configured ? '已就绪' : '未完成' }}
              </Badge>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">自动守护</dt>
            <dd>
              <Badge :tone="guardian.autoEnabled ? 'success' : 'gray'" dot>
                {{ guardian.autoEnabled ? '运行中' : '已暂停' }}
              </Badge>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">真实流量样本</dt>
            <dd>
              <Badge :tone="guardian.monitoringEnabled ? 'success' : 'warning'" dot>
                {{ guardian.monitoringEnabled ? '已接入' : '未开启' }}
              </Badge>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">实时推送</dt>
            <dd>
              <Badge :tone="guardian.connected ? 'success' : 'gray'" dot>
                {{ guardian.connected ? '已连接' : '轮询兜底' }}
              </Badge>
            </dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">上次调度</dt>
            <dd class="text-gray-900 dark:text-white">{{ formatRelative(status?.last_run_at) }}</dd>
          </div>
          <div class="flex items-center justify-between gap-3">
            <dt class="text-gray-500 dark:text-dark-400">耗时</dt>
            <dd class="text-gray-900 dark:text-white">{{ status?.last_run_ms ?? 0 }} ms</dd>
          </div>
          <div v-if="status?.last_run_error" class="rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">
            {{ status.last_run_error }}
          </div>

          <div class="border-t border-gray-100 pt-3 dark:border-dark-700">
            <p class="mb-2 text-xs font-medium text-gray-500 dark:text-dark-400">上一轮概要</p>
            <div class="grid grid-cols-2 gap-2 text-xs">
              <span class="text-gray-500 dark:text-dark-400">受管渠道</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.channels }}</span>
              <span class="text-gray-500 dark:text-dark-400">本轮探测</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.probed }}</span>
              <span class="text-gray-500 dark:text-dark-400">新增样本</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.samples }}</span>
              <span class="text-gray-500 dark:text-dark-400">熔断 / 回池</span>
              <span class="text-right text-gray-900 dark:text-white">
                {{ summary.fused }} / {{ summary.recovered }}
              </span>
              <span class="text-gray-500 dark:text-dark-400">写回渠道</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.applied }}</span>
              <span class="text-gray-500 dark:text-dark-400">自动处置</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.cleaned_up }}</span>
              <span class="text-gray-500 dark:text-dark-400">告警</span>
              <span class="text-right text-gray-900 dark:text-white">{{ summary.alerts }}</span>
            </div>
          </div>
        </dl>
      </section>
    </div>

    <section class="card">
      <div class="card-header">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">最近写回记录</h2>
        <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
          Guardian 对 sub2api 的每一次写操作都有前后值记录，便于排查与追溯。
        </p>
      </div>
      <div class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th class="w-40">时间</th>
              <th class="w-56">渠道</th>
              <th class="w-40">分组</th>
              <th class="w-40">操作</th>
              <th>变更</th>
              <th class="w-24">结果</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="action in actions" :key="action.id">
              <td class="whitespace-nowrap text-xs">{{ formatTime(action.created_at) }}</td>
              <td>
                <div class="w-52 max-w-[13rem]">
                  <p
                    class="truncate text-xs font-medium"
                    :class="action.deleted
                      ? 'text-gray-400 line-through dark:text-dark-500'
                      : 'text-gray-900 dark:text-white'"
                    :title="action.account_name"
                  >
                    {{ action.account_name }}
                  </p>
                  <p class="truncate text-xs text-gray-400 dark:text-dark-500">
                    #{{ action.account_id }}<span v-if="action.platform"> · {{ action.platform }}</span>
                  </p>
                </div>
              </td>
              <td>
                <p
                  class="w-36 max-w-[9rem] truncate text-xs text-gray-500 dark:text-dark-400"
                  :title="actionGroups(action)"
                >
                  {{ actionGroups(action) }}
                </p>
              </td>
              <td class="text-xs"><span class="code">{{ action.kind }}</span></td>
              <td class="text-xs">
                <span class="text-gray-500 dark:text-dark-400">{{ action.before || '—' }}</span>
                <span class="mx-1 text-gray-400">→</span>
                <span class="text-gray-900 dark:text-white">{{ action.after || '—' }}</span>
              </td>
              <td>
                <Badge :tone="action.ok ? 'success' : 'danger'">{{ action.ok ? '成功' : '失败' }}</Badge>
                <p v-if="action.error" class="mt-1 max-w-xs truncate text-xs text-red-500" :title="action.error">
                  {{ action.error }}
                </p>
              </td>
            </tr>
          </tbody>
        </table>
        <EmptyState v-if="!actions.length" icon="document" title="还没有写回记录" />
      </div>
    </section>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import Field from '@/components/Field.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import { formatRelative, formatTime } from '@/lib/format'
import type { Action } from '@/lib/types'

const guardian = useGuardianStore()
const ui = useUIStore()

const form = reactive({
  baseURL: '',
  adminKey: '',
  timeout: 60,
  enabled: true
})
const hasKey = ref(false)
const actions = ref<Action[]>([])

const status = computed(() => guardian.status)
const summary = computed(
  () =>
    status.value?.last_summary ?? {
      channels: 0,
      probed: 0,
      samples: 0,
      fused: 0,
      recovered: 0,
      applied: 0,
      cleaned_up: 0,
      alerts: 0
    }
)

onMounted(async () => {
  try {
    const conn = await guardian.loadConnection()
    if (conn) {
      form.baseURL = conn.base_url
      form.timeout = conn.timeout_seconds
      form.enabled = conn.enabled
      hasKey.value = conn.has_admin_key
    }
    actions.value = (await api.actions()).items ?? []
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
})

function actionGroups(action: Action): string {
  return action.groups?.map(group => group.name).join(' / ') || '—'
}

async function persist() {
  const payload: Record<string, unknown> = {
    base_url: form.baseURL,
    timeout_seconds: form.timeout,
    enabled: form.enabled
  }
  // 留空表示不修改已保存的 Key。
  if (form.adminKey.trim()) payload.admin_api_key = form.adminKey.trim()

  const conn = await api.saveConnection(payload)
  hasKey.value = conn.has_admin_key
  form.adminKey = ''
  await guardian.refresh({ silent: true })
}

async function save() {
  try {
    await guardian.run(() => persist())
    ui.notify('success', '连接配置已保存')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function testConnection() {
  try {
    await guardian.run(async () => {
      await persist()
      await api.sync()
    })
    ui.notify('success', '连接正常，已同步分组与账号')
    actions.value = (await api.actions()).items ?? []
  } catch (err) {
    ui.notify('error', `连接测试失败：${(err as Error).message}`)
  }
}
</script>
