<template>
  <AppLayout title="通知配置" subtitle="统一管理所有定时报告共用的企业微信通知">
    <div v-if="loading" class="card flex min-h-64 items-center justify-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载通知配置…
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-xl font-semibold text-gray-900 dark:text-white">通知配置</h2>
            <Badge :tone="form.wecom.enabled ? (form.wecom.secret ? 'success' : 'warning') : 'gray'" dot>
              {{ form.wecom.enabled ? (form.wecom.secret ? '已启用' : '待完善') : '未启用' }}
            </Badge>
          </div>
          <p class="mt-1 max-w-3xl text-sm leading-relaxed text-gray-500 dark:text-dark-400">
            这里的企微应用配置供所有定时报告共用；渠道管理中的企微告警配置仍保持独立。
          </p>
        </div>
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
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
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">企业微信应用</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            定时报告告警和查询失败时使用普通文本消息发送，正常运行不会发送静默消息。
          </p>
        </div>
        <form class="space-y-5 p-6" @submit.prevent="save">
          <SwitchRow
            v-model="form.wecom.enabled"
            label="启用企微通知"
            description="启用后，满足报告告警条件或报告查询失败时发送通知。"
          />
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field v-model="form.wecom.corp_id" label="CorpID" placeholder="ww..." />
            <Field v-model="form.wecom.agent_id" label="AgentID" type="number" :min="1" />
            <Field
              v-model="form.wecom.secret"
              label="Secret"
              type="text"
              placeholder="请输入应用 Secret"
              hint="Secret 按明文显示；留空提交不会覆盖已保存值。"
            />
            <Field
              v-model="form.wecom.target"
              label="接收人"
              placeholder="@all 或 zhangsan|lisi"
              hint="支持 @all；多个成员 ID 使用 | 分隔。"
            />
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="testWeCom">
              <Icon name="chat" size="sm" />
              {{ testing ? '发送中…' : '测试企微' }}
            </button>
            <span class="text-xs text-gray-500 dark:text-dark-400">测试使用已保存的通知配置。</span>
          </div>
        </form>
      </section>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import SwitchRow from '@/components/SwitchRow.vue'
import { api } from '@/lib/api'
import type { ReportNotificationConfig, ReportNotificationSaveInput } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

type NotificationForm = {
  wecom: ReportNotificationSaveInput['wecom'] & { has_secret: boolean }
}

const ui = useUIStore()
const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const error = ref('')

const form = ref<NotificationForm>({
  wecom: { enabled: false, corp_id: '', agent_id: 0, secret: '', target: '', has_secret: false }
})

const busy = computed(() => saving.value || testing.value)

onMounted(() => void load())

async function load() {
  loading.value = true
  error.value = ''
  try {
    applyConfig(await api.reportNotificationSettings())
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function applyConfig(config: ReportNotificationConfig) {
  form.value = {
    wecom: {
      enabled: config.wecom.enabled,
      corp_id: config.wecom.corp_id,
      agent_id: config.wecom.agent_id,
      secret: config.wecom.secret,
      target: config.wecom.target,
      has_secret: config.wecom.has_secret
    }
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const config = await api.saveReportNotificationSettings({
      wecom: {
        enabled: form.value.wecom.enabled,
        corp_id: form.value.wecom.corp_id,
        agent_id: Number(form.value.wecom.agent_id),
        secret: form.value.wecom.secret,
        target: form.value.wecom.target
      }
    })
    applyConfig(config)
    ui.notify('success', '通知配置已保存')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    saving.value = false
  }
}

async function testWeCom() {
  testing.value = true
  error.value = ''
  try {
    const result = await api.testReportNotificationWeCom()
    ui.notify('success', result.message_id ? `企微测试消息已发送（${result.message_id}）` : '企微测试消息已发送')
  } catch (err) {
    error.value = (err as Error).message
    ui.notify('error', error.value)
  } finally {
    testing.value = false
  }
}
</script>
