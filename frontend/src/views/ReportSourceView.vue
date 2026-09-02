<template>
  <AppLayout title="源站配置" subtitle="管理定时报告可选的数据源">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载源站配置…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">报告源站</h2>
            <Badge tone="gray">{{ customSourceCount }} 个自定义源站</Badge>
          </div>
        </div>
        <button type="button" class="btn btn-primary btn-sm" @click="openCreate">
          <Icon name="plus" size="sm" />
          新增源站
        </button>
      </div>

      <div
        v-if="error"
        class="flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
      >
        <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
        <span>{{ error }}</span>
      </div>

      <section class="card">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">可用源站</h2>
        </div>
        <div class="table-wrapper">
          <table class="table min-w-[720px]">
            <thead>
              <tr>
                <th>名称</th>
                <th>类型</th>
                <th>连接地址</th>
                <th>状态</th>
                <th class="w-28">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="source in sources" :key="source.id">
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-medium text-gray-900 dark:text-white">{{ source.name }}</span>
                    <Badge v-if="source.mode === 'global'" tone="gray">全局</Badge>
                  </div>
                </td>
                <td>{{ sourceTypeLabel(source.source_type) }}</td>
                <td class="max-w-md">
                  <span class="block truncate text-xs text-gray-500 dark:text-dark-400" :title="source.effective_base_url">
                    {{ source.effective_base_url || '未配置' }}
                  </span>
                </td>
                <td>
                  <Badge :tone="source.configured ? 'success' : 'danger'" dot>
                    {{ source.configured ? '可用' : '未配置' }}
                  </Badge>
                </td>
                <td>
                  <RouterLink
                    v-if="source.mode === 'global'"
                    to="/connection"
                    class="btn btn-ghost btn-sm"
                  >
                    连接设置
                  </RouterLink>
                  <div v-else class="flex items-center gap-1">
                    <button
                      type="button"
                      class="btn btn-ghost btn-icon"
                      title="编辑源站"
                      aria-label="编辑源站"
                      @click="openEdit(source)"
                    >
                      <Icon name="edit" size="sm" />
                    </button>
                    <button
                      type="button"
                      class="btn btn-ghost btn-icon text-red-500 hover:text-red-600"
                      title="删除源站"
                      aria-label="删除源站"
                      :disabled="deletingID === source.id"
                      @click="removeSource(source)"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <Modal
      :open="editorOpen"
      :title="editingID ? '编辑源站' : '新增源站'"
      @close="closeEditor"
    >
      <form id="report-source-form" class="space-y-4" @submit.prevent="saveSource">
        <Field v-model="form.name" label="源站名称" placeholder="例如：生产站" />
        <label class="block">
          <span class="input-label">源站类型</span>
          <select v-model="form.source_type" class="input">
            <option value="sub2api">Sub2API</option>
            <option value="newapi">New API</option>
          </select>
        </label>
        <Field
          v-model="form.base_url"
          label="源站地址"
          placeholder="https://api.example.com"
          inputmode="url"
        />
        <Field
          v-model="form.credential"
          :label="credentialLabel"
          type="password"
          autocomplete="new-password"
          :placeholder="editingID ? '已配置，留空保持不变' : credentialPlaceholder"
        />
        <Field
          v-if="form.source_type === 'newapi'"
          v-model="form.newapi_user_id"
          label="New API 用户 ID"
          type="number"
          :min="1"
        />
      </form>

      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeEditor">取消</button>
        <button type="submit" form="report-source-form" class="btn btn-primary" :disabled="saving">
          <Icon name="check" size="sm" />
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import Modal from '@/components/Modal.vue'
import { api } from '@/lib/api'
import type {
  ReportSourceConfig,
  ReportSourceItem,
  ReportSourceSaveInput,
  ReportSourceType
} from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const deletingID = ref('')
const config = ref<ReportSourceConfig | null>(null)
const editorOpen = ref(false)
const editingID = ref('')
const form = ref<ReportSourceSaveInput>({
  name: '',
  source_type: 'sub2api',
  base_url: '',
  credential: '',
  newapi_user_id: 0
})

const sources = computed(() => config.value?.items ?? [])
const customSourceCount = computed(() => sources.value.filter(source => source.mode === 'custom').length)
const credentialLabel = computed(() => form.value.source_type === 'newapi' ? '系统访问令牌' : 'Admin API Key')
const credentialPlaceholder = computed(() => form.value.source_type === 'newapi' ? '请输入系统访问令牌' : '请输入 Admin API Key')

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    config.value = await api.reportSourceSettings()
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function resetForm() {
  form.value = {
    name: '',
    source_type: 'sub2api',
    base_url: '',
    credential: '',
    newapi_user_id: 0
  }
}

function openCreate() {
  editingID.value = ''
  resetForm()
  editorOpen.value = true
}

function openEdit(source: ReportSourceItem) {
  editingID.value = source.id
  form.value = {
    id: source.id,
    name: source.name,
    source_type: source.source_type,
    base_url: source.base_url,
    credential: '',
    newapi_user_id: source.newapi_user_id
  }
  editorOpen.value = true
}

function closeEditor() {
  if (!saving.value) editorOpen.value = false
}

async function saveSource() {
  saving.value = true
  error.value = ''
  try {
    config.value = await api.saveReportSourceSettings({
      id: editingID.value || undefined,
      name: form.value.name,
      source_type: form.value.source_type,
      base_url: form.value.base_url,
      credential: form.value.credential,
      newapi_user_id: Number(form.value.newapi_user_id)
    })
    editorOpen.value = false
    ui.notify('success', editingID.value ? '源站已更新' : '源站已添加')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    saving.value = false
  }
}

async function removeSource(source: ReportSourceItem) {
  if (!window.confirm(`确定删除源站「${source.name}」吗？`)) return
  deletingID.value = source.id
  error.value = ''
  try {
    config.value = await api.deleteReportSource(source.id)
    ui.notify('success', '源站已删除')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    deletingID.value = ''
  }
}

function sourceTypeLabel(value: ReportSourceType) {
  return value === 'newapi' ? 'New API' : 'Sub2API'
}
</script>
