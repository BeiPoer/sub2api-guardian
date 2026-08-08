<template>
  <AppLayout title="image2路由" subtitle="管理 OpenAI Images API 上游与 Base64 图片 URL 转换">
    <section class="card">
      <div class="card-header flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">全局设置</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            <span :class="settings.hasProxyKey ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'">
              {{ settings.hasProxyKey ? '代理鉴权已配置' : '代理鉴权未配置' }}
            </span>
          </p>
        </div>
      </div>

      <form class="grid grid-cols-1 gap-4 p-6 lg:grid-cols-4" @submit.prevent="saveSettings">
        <div class="lg:col-span-2">
          <Field
            v-model="settings.imageDomain"
            label="图片域名"
            placeholder="img.example.com"
          />
        </div>
        <Field
          v-model="settings.retentionHours"
          label="图片保留时长"
          type="number"
          suffix="小时"
          :min="1"
        />
        <Field
          v-model="settings.proxyAPIKey"
          label="代理 API Key"
          type="password"
          :placeholder="settings.hasProxyKey ? '已配置，留空则不修改' : '必填'"
        />
        <div class="flex items-center lg:col-span-4">
          <button type="submit" class="btn btn-primary" :disabled="savingSettings">
            <Icon name="check" size="sm" />
            {{ savingSettings ? '保存中…' : '保存设置' }}
          </button>
        </div>
      </form>
    </section>

    <section class="card">
      <div class="card-header flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">上游路由</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">{{ upstreams.length }} 个上游</p>
        </div>
        <button type="button" class="btn btn-primary" @click="openCreate">
          <Icon name="plus" size="sm" />
          添加上游
        </button>
      </div>

      <div v-if="loading" class="flex min-h-48 items-center justify-center">
        <span class="spinner text-primary-500" />
      </div>
      <div v-else-if="upstreams.length" class="overflow-x-auto">
        <table class="table min-w-[980px]">
          <thead>
            <tr>
              <th>名称</th>
              <th>代理 Base URL</th>
              <th>上游 Base URL</th>
              <th>模型映射</th>
              <th class="w-24">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="upstream in upstreams" :key="upstream.id">
              <td>
                <p class="font-medium text-gray-900 dark:text-white">{{ upstream.name }}</p>
                <p class="mt-0.5 text-xs text-gray-400 dark:text-dark-500">{{ upstream.slug }}</p>
              </td>
              <td>
                <div class="flex max-w-sm items-center gap-1.5">
                  <code class="code truncate" :title="proxyBaseURL(upstream)">{{ proxyBaseURL(upstream) }}</code>
                  <button
                    type="button"
                    class="btn btn-ghost btn-icon flex-shrink-0"
                    title="复制代理地址"
                    aria-label="复制代理地址"
                    @click="copyProxyURL(upstream)"
                  >
                    <Icon name="copy" size="xs" />
                  </button>
                </div>
              </td>
              <td>
                <code class="block max-w-xs truncate text-xs" :title="upstream.base_url">{{ upstream.base_url }}</code>
              </td>
              <td>
                <Badge :tone="upstream.model_mapping ? 'purple' : 'gray'">
                  {{ upstream.model_mapping ? `${mappingCount(upstream)} 条` : '未配置' }}
                </Badge>
              </td>
              <td>
                <div class="flex items-center gap-1">
                  <button
                    type="button"
                    class="btn btn-ghost btn-icon"
                    title="编辑"
                    aria-label="编辑上游"
                    @click="openEdit(upstream)"
                  >
                    <Icon name="edit" size="sm" />
                  </button>
                  <button
                    type="button"
                    class="btn btn-ghost btn-icon text-red-500 hover:text-red-600"
                    title="删除"
                    aria-label="删除上游"
                    :disabled="deletingID === upstream.id"
                    @click="removeUpstream(upstream)"
                  >
                    <Icon name="trash" size="sm" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState v-else icon="beaker" title="暂无上游路由">
        <button type="button" class="btn btn-primary mt-4" @click="openCreate">
          <Icon name="plus" size="sm" />
          添加上游
        </button>
      </EmptyState>
    </section>

    <Modal :open="modalOpen" :title="editingID ? '编辑上游' : '添加上游'" @close="closeModal">
      <form id="image2-upstream-form" class="space-y-4" @submit.prevent="saveUpstream">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field v-model="upstreamForm.name" label="显示名称" />
          <Field v-model="upstreamForm.slug" label="URL 标识" placeholder="primary" />
        </div>
        <Field
          v-model="upstreamForm.baseURL"
          label="上游 Base URL"
          placeholder="https://api.openai.com"
        />
        <Field
          v-model="upstreamForm.apiKey"
          label="上游 API Key"
          type="password"
          :placeholder="editingID ? '已配置，留空则不修改' : '必填'"
        />
        <label class="block">
          <span class="input-label">模型映射</span>
          <textarea
            v-model="upstreamForm.modelMapping"
            class="input min-h-28 resize-y font-mono"
            placeholder="gpt-image-2=gpt-image-2-provider"
          />
        </label>
      </form>

      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="savingUpstream" @click="closeModal">取消</button>
        <button
          type="submit"
          form="image2-upstream-form"
          class="btn btn-primary"
          :disabled="savingUpstream"
        >
          <Icon name="check" size="sm" />
          {{ savingUpstream ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import EmptyState from '@/components/EmptyState.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import Modal from '@/components/Modal.vue'
import { api } from '@/lib/api'
import type { Image2Upstream } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
const loading = ref(true)
const savingSettings = ref(false)
const savingUpstream = ref(false)
const deletingID = ref<number | null>(null)
const modalOpen = ref(false)
const editingID = ref<number | null>(null)
const upstreams = ref<Image2Upstream[]>([])

const settings = reactive({
  imageDomain: '',
  retentionHours: 24,
  proxyAPIKey: '',
  hasProxyKey: false
})

const upstreamForm = reactive({
  name: '',
  slug: '',
  baseURL: '',
  apiKey: '',
  modelMapping: ''
})

onMounted(load)

async function load() {
  loading.value = true
  try {
    const data = await api.image2()
    settings.imageDomain = data.settings.image_domain
    settings.retentionHours = data.settings.retention_hours
    settings.hasProxyKey = data.settings.has_proxy_api_key
    upstreams.value = data.upstreams ?? []
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  savingSettings.value = true
  try {
    const payload: {
      image_domain: string
      retention_hours: number
      proxy_api_key?: string
    } = {
      image_domain: settings.imageDomain.trim(),
      retention_hours: settings.retentionHours
    }
    if (settings.proxyAPIKey.trim()) payload.proxy_api_key = settings.proxyAPIKey.trim()
    const saved = await api.saveImage2Settings(payload)
    settings.imageDomain = saved.image_domain
    settings.retentionHours = saved.retention_hours
    settings.hasProxyKey = saved.has_proxy_api_key
    settings.proxyAPIKey = ''
    ui.notify('success', 'image2 全局设置已保存')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    savingSettings.value = false
  }
}

function resetUpstreamForm() {
  Object.assign(upstreamForm, {
    name: '',
    slug: '',
    baseURL: '',
    apiKey: '',
    modelMapping: ''
  })
}

function openCreate() {
  editingID.value = null
  resetUpstreamForm()
  modalOpen.value = true
}

function openEdit(upstream: Image2Upstream) {
  editingID.value = upstream.id
  Object.assign(upstreamForm, {
    name: upstream.name,
    slug: upstream.slug,
    baseURL: upstream.base_url,
    apiKey: '',
    modelMapping: upstream.model_mapping
  })
  modalOpen.value = true
}

function closeModal() {
  if (savingUpstream.value) return
  modalOpen.value = false
}

async function saveUpstream() {
  if (!upstreamForm.name.trim() || !upstreamForm.slug.trim() || !upstreamForm.baseURL.trim()) {
    ui.notify('error', '请填写显示名称、URL 标识和上游 Base URL')
    return
  }
  if (!editingID.value && !upstreamForm.apiKey.trim()) {
    ui.notify('error', '请填写上游 API Key')
    return
  }

  savingUpstream.value = true
  try {
    const payload = {
      name: upstreamForm.name.trim(),
      slug: upstreamForm.slug.trim(),
      base_url: upstreamForm.baseURL.trim(),
      api_key: upstreamForm.apiKey.trim(),
      model_mapping: upstreamForm.modelMapping
    }
    if (editingID.value) {
      const updated = await api.updateImage2Upstream(editingID.value, payload)
      upstreams.value = upstreams.value.map(item => (item.id === updated.id ? updated : item))
      ui.notify('success', '上游已更新')
    } else {
      upstreams.value.push(await api.createImage2Upstream(payload))
      ui.notify('success', '上游已添加')
    }
    modalOpen.value = false
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    savingUpstream.value = false
  }
}

async function removeUpstream(upstream: Image2Upstream) {
  if (!window.confirm(`确定删除上游「${upstream.name}」吗？`)) return
  deletingID.value = upstream.id
  try {
    await api.deleteImage2Upstream(upstream.id)
    upstreams.value = upstreams.value.filter(item => item.id !== upstream.id)
    ui.notify('success', '上游已删除')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    deletingID.value = null
  }
}

function proxyBaseURL(upstream: Image2Upstream): string {
  const origin = settings.imageDomain.trim()
    ? `${window.location.protocol}//${settings.imageDomain.trim().replace(/\/+$/, '')}`
    : window.location.origin
  return `${origin}/${encodeURIComponent(upstream.slug)}/v1`
}

async function copyProxyURL(upstream: Image2Upstream) {
  try {
    await navigator.clipboard.writeText(proxyBaseURL(upstream))
    ui.notify('success', '代理地址已复制')
  } catch {
    ui.notify('error', '复制失败，请手动选择地址')
  }
}

function mappingCount(upstream: Image2Upstream): number {
  return upstream.model_mapping.split('\n').filter(Boolean).length
}
</script>
