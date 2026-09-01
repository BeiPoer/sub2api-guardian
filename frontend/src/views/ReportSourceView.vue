<template>
  <AppLayout title="源站配置" subtitle="统一管理渠道使用报告和每日报告读取数据的源站">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载源站配置…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">源站配置</h2>
            <Badge :tone="config?.configured ? 'success' : 'warning'" dot>
              {{ config?.configured ? '已配置' : '待完善' }}
            </Badge>
          </div>
          <p class="mt-1 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
            两类定时报告共享这一源站；全局模式跟随项目连接设置，自定义模式使用这里单独保存的凭据。
          </p>
        </div>
        <button type="button" class="btn btn-primary btn-sm" :disabled="saving" @click="save">
          <Icon name="check" size="sm" />
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </div>

      <div
        v-if="error"
        class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
      >
        <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
        <span>{{ error }}</span>
      </div>

      <section class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">源站模式</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">选择报告使用项目全局连接，或使用独立的自定义源站。</p>
        </div>
        <div class="space-y-6 p-6">
          <SegmentedControl v-model="form.mode" :options="modeOptions" />

          <div v-if="form.mode === 'global'" class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div class="min-w-0 rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
              <p class="text-xs text-gray-500 dark:text-dark-400">当前有效地址</p>
              <p class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">
                {{ config?.mode === 'global' ? (config.effective_base_url || '未配置') : '保存后读取全局连接' }}
              </p>
            </div>
            <div class="rounded-lg border border-gray-100 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
              <p class="text-xs text-gray-500 dark:text-dark-400">配置状态</p>
              <div class="mt-1 flex flex-wrap items-center gap-2">
                <Badge v-if="config?.mode === 'global'" :tone="config.configured ? 'success' : 'danger'" dot>
                  {{ config.configured ? '全局 Sub2API 可用' : '全局连接未完成' }}
                </Badge>
                <Badge v-else tone="gray">保存后生效</Badge>
                <RouterLink
                  v-if="config?.mode === 'global' && !config.configured"
                  to="/connection"
                  class="text-xs text-primary-600 hover:underline dark:text-primary-300"
                >
                  前往连接设置
                </RouterLink>
              </div>
            </div>
          </div>

          <div v-else class="space-y-5">
            <div>
              <p class="input-label">源站类型</p>
              <SegmentedControl v-model="form.source_type" :options="typeOptions" />
            </div>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div class="sm:col-span-2">
                <Field
                  v-model="form.base_url"
                  label="源站地址"
                  placeholder="https://api.example.com"
                  inputmode="url"
                  hint="仅支持 HTTP(S) 绝对地址，保存时会移除末尾斜杠。"
                />
              </div>
              <Field
                v-model="form.credential"
                :label="credentialLabel"
                type="password"
                autocomplete="new-password"
                :placeholder="credentialPlaceholder"
                :hint="config?.has_credential && config.source_type === form.source_type ? '已保存凭据；留空会保持不变。' : '保存自定义源站时必须填写。'"
              />
              <Field
                v-if="form.source_type === 'newapi'"
                v-model="form.newapi_user_id"
                label="New API 用户 ID"
                type="number"
                :min="1"
                hint="请求会同时发送 New-Api-User，以兼容仍要求该请求头的版本。"
              />
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">当前生效源站</h2>
        </div>
        <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-3">
          <div class="min-w-0">
            <p class="text-xs text-gray-500 dark:text-dark-400">模式</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ config?.mode === 'global' ? '全局 Sub2API' : '自定义源站' }}</p>
          </div>
          <div class="min-w-0">
            <p class="text-xs text-gray-500 dark:text-dark-400">类型</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ sourceTypeLabel(config?.effective_type) }}</p>
          </div>
          <div class="min-w-0">
            <p class="text-xs text-gray-500 dark:text-dark-400">地址</p>
            <p class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">{{ config?.effective_base_url || '未配置' }}</p>
          </div>
        </div>
      </section>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { api } from '@/lib/api'
import type { ReportSourceConfig, ReportSourceSaveInput, ReportSourceType } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const config = ref<ReportSourceConfig | null>(null)
const applying = ref(false)

const form = ref<ReportSourceSaveInput>({
  mode: 'global',
  source_type: 'sub2api',
  base_url: '',
  credential: '',
  newapi_user_id: 0
})

const modeOptions = [
  { value: 'global', label: '全局 Sub2API', icon: 'link' as const },
  { value: 'custom', label: '自定义源站', icon: 'globe' as const }
]

const typeOptions = [
  { value: 'sub2api', label: 'Sub2API', icon: 'server' as const },
  { value: 'newapi', label: 'New API', icon: 'database' as const }
]

const credentialLabel = computed(() => form.value.source_type === 'newapi' ? '系统访问令牌' : 'Admin API Key')
const credentialPlaceholder = computed(() => {
  if (config.value?.has_credential && config.value.source_type === form.value.source_type) {
    return '已配置，留空保持不变'
  }
  return form.value.source_type === 'newapi' ? '请输入系统访问令牌' : '请输入 Admin API Key'
})

watch(() => form.value.source_type, (value, previous) => {
  if (!applying.value && value !== previous) form.value.credential = ''
})

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    applyConfig(await api.reportSourceSettings())
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function applyConfig(value: ReportSourceConfig) {
  applying.value = true
  config.value = value
  form.value = {
    mode: value.mode,
    source_type: value.source_type,
    base_url: value.base_url,
    credential: '',
    newapi_user_id: value.newapi_user_id
  }
  applying.value = false
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const saved = await api.saveReportSourceSettings({
      mode: form.value.mode,
      source_type: form.value.source_type,
      base_url: form.value.base_url,
      credential: form.value.credential,
      newapi_user_id: Number(form.value.newapi_user_id)
    })
    applyConfig(saved)
    ui.notify('success', '源站配置已保存')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    saving.value = false
  }
}

function sourceTypeLabel(value?: ReportSourceType) {
  return value === 'newapi' ? 'New API' : 'Sub2API'
}
</script>
