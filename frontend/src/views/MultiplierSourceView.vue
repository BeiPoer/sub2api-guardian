<template>
  <AppLayout title="倍率同步源站" subtitle="选择本机渠道管理，或从另一套 Guardian 读取渠道管理倍率">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="spinner text-primary-500" />正在加载倍率源配置
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">倍率同步源站</h2>
            <Badge :tone="statusTone" dot>{{ statusLabel }}</Badge>
          </div>
          <p class="mt-1 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
            渠道管理只维护一份；当前 Guardian 会把匹配到的倍率应用到本地渠道池并更新名称后缀。
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button v-if="form.mode === 'remote'" type="button" class="btn btn-secondary btn-sm" :disabled="busy || !config?.has_authorization" @click="test">
            <Icon name="link" size="sm" />测试连接
          </button>
          <button v-if="form.mode === 'remote'" type="button" class="btn btn-secondary btn-sm" :disabled="busy || !config?.has_authorization" @click="sync">
            <Icon name="refresh" size="sm" :class="busyAction === 'sync' && 'animate-spin'" />立即同步
          </button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
            <Icon name="check" size="sm" />{{ form.mode === 'remote' && form.password ? '保存并授权' : '保存' }}
          </button>
        </div>
      </div>

      <div v-if="error" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
        <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
        <span class="min-w-0 flex-1">{{ error }}</span>
      </div>

      <section class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">倍率来源</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">远程模式只保存 G1 自动签发的只读授权，不保存 G1 密码。</p>
        </div>
        <div class="space-y-5 p-6">
          <SegmentedControl v-model="form.mode" :options="modeOptions" />

          <div v-if="form.mode === 'local'" class="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
              <p class="text-xs text-gray-500 dark:text-dark-400">本机倍率源</p>
              <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">渠道管理</p>
            </div>
            <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
              <p class="text-xs text-gray-500 dark:text-dark-400">状态</p>
              <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ localStatusLabel }}</p>
            </div>
            <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
              <p class="text-xs text-gray-500 dark:text-dark-400">已索引令牌</p>
              <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ config?.local_status.indexed_tokens ?? 0 }}</p>
            </div>
          </div>

          <div v-else class="space-y-4">
            <Field v-model="form.base_url" label="G1 Guardian 地址" placeholder="https://g1.example.com" inputmode="url" hint="填写 G1 面板根地址，不要附加 /api 路径。" />
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field v-model="form.username" label="G1 用户名" autocomplete="username" />
              <Field v-model="form.password" label="G1 密码" type="password" autocomplete="current-password" :placeholder="config?.has_authorization ? '已授权，留空保持不变' : '用于首次授权'" hint="密码只用于授权请求，G2 不保存密码。" />
              <Field v-model="form.timeout_seconds" label="请求超时" suffix="秒" type="number" :min="3" :max="120" />
            </div>
            <div v-if="config?.has_authorization" class="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border border-gray-100 bg-gray-50/60 px-4 py-3 text-sm dark:border-dark-700 dark:bg-dark-900/40">
              <span class="text-gray-500 dark:text-dark-400">授权状态 <strong class="font-medium text-gray-900 dark:text-white">已授权</strong></span>
              <span class="text-gray-500 dark:text-dark-400">源站 <strong class="font-medium text-gray-900 dark:text-white">{{ config?.source_id || 'G1' }}</strong></span>
              <span class="text-gray-500 dark:text-dark-400">版本 <strong class="font-medium text-gray-900 dark:text-white">{{ shortRevision }}</strong></span>
            </div>
          </div>
        </div>
      </section>

      <section v-if="form.mode === 'remote'" class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">同步状态</h2>
        </div>
        <dl class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <dt class="text-xs text-gray-500 dark:text-dark-400">最近状态</dt>
            <dd class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ remoteStateLabel }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-dark-400">最近成功同步</dt>
            <dd class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ relative(config?.last_success_at) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-dark-400">匹配账号</dt>
            <dd class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ config?.last_matched ?? 0 }} / {{ config?.last_total ?? 0 }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-dark-400">完整快照</dt>
            <dd class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ config?.last_complete ? '是' : '否' }}</dd>
          </div>
        </dl>
        <div v-if="config?.last_error" class="border-t border-gray-100 px-6 py-4 text-sm text-red-600 dark:border-dark-700 dark:text-red-300">{{ config.last_error }}</div>
        <div class="flex justify-end border-t border-gray-100 px-6 py-4 dark:border-dark-700">
          <button type="button" class="btn btn-ghost btn-sm text-red-600 hover:text-red-700 dark:text-red-300" :disabled="busy || !config?.has_authorization" @click="clear">
            <Icon name="x" size="sm" />解除 G1 授权
          </button>
        </div>
      </section>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { api } from '@/lib/api'
import { formatRelative } from '@/lib/format'
import type { MultiplierSourceConfig, MultiplierSourceMode } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const loading = ref(true)
const busy = ref(false)
const busyAction = ref('')
const error = ref('')
const config = ref<MultiplierSourceConfig | null>(null)
const form = reactive({
  mode: 'local' as MultiplierSourceMode,
  base_url: '',
  username: '',
  password: '',
  timeout_seconds: 10
})

const modeOptions = [
  { value: 'local', label: '本机渠道管理', icon: 'server' as const },
  { value: 'remote', label: 'G1 Guardian', icon: 'link' as const }
]

const statusLabel = computed(() => {
  if (form.mode === 'local') return localStatusLabel.value
  if (!config.value?.has_authorization) return '待授权'
  return remoteStateLabel.value
})
const statusTone = computed(() => {
  if (form.mode === 'local') return config.value?.local_status.complete ? 'success' : 'warning'
  if (!config.value?.has_authorization) return 'warning'
  return config.value?.last_state === 'ready' ? 'success' : config.value?.last_state === 'error' ? 'danger' : 'warning'
})
const localStatusLabel = computed(() => statusText(config.value?.local_status.state))
const remoteStateLabel = computed(() => statusText(config.value?.last_state))
const shortRevision = computed(() => {
  const value = config.value?.last_revision || ''
  return value ? value.slice(0, 12) : '暂无'
})

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    apply(await api.multiplierSource())
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function apply(value: MultiplierSourceConfig) {
  config.value = value
  form.mode = value.mode
  form.base_url = value.base_url
  form.username = value.username
  form.password = ''
  form.timeout_seconds = value.timeout_seconds || 10
}

async function save() {
  await run(async () => {
    if (form.mode === 'remote' && form.password) {
      const result = await api.authorizeMultiplierSource({
        base_url: form.base_url,
        username: form.username,
        password: form.password,
        timeout_seconds: Number(form.timeout_seconds)
      })
      apply(result.config)
      if (result.sync_error) {
        error.value = result.sync_error
        ui.notify('warning', `G1 已授权，但首次同步失败：${result.sync_error}`)
      } else {
        ui.notify('success', 'G1 已授权并完成倍率同步')
      }
      return
    }
    apply(await api.saveMultiplierSource({
      mode: form.mode,
      base_url: form.base_url,
      username: form.username,
      timeout_seconds: Number(form.timeout_seconds)
    }))
    ui.notify('success', '倍率源配置已保存')
  })
}

async function test() {
  await run(async () => {
    const result = await api.testMultiplierSource()
    apply(result.config)
    ui.notify('success', `G1 连接正常：${statusText((result.status as { state?: string }).state)}`)
  }, 'test')
}

async function sync() {
  await run(async () => {
    const result = await api.syncMultiplierSource()
    apply(result.config)
    ui.notify('success', `倍率同步完成：匹配 ${result.result.matched} / ${result.result.total}`)
  }, 'sync')
}

async function clear() {
  if (!window.confirm('解除 G1 授权并清理由 G1 管理的倍率和名称后缀吗？')) return
  await run(async () => {
    apply(await api.clearMultiplierSource())
    ui.notify('success', '已解除 G1 授权')
  }, 'clear')
}

async function run(action: () => Promise<void>, name = 'save') {
  busy.value = true
  busyAction.value = name
  error.value = ''
  try {
    await action()
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    busy.value = false
    busyAction.value = ''
  }
}

function relative(value?: string) {
  return value ? formatRelative(value) : '暂无'
}

function statusText(value?: string) {
  switch (value) {
    case 'ready': return '已就绪'
    case 'partial': return '部分就绪'
    case 'authorized': return '已授权'
    case 'error': return '同步失败'
    case 'not_ready': return '未就绪'
    default: return '未同步'
  }
}
</script>
