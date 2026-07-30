<template>
  <div class="relative min-h-screen bg-gray-50 dark:bg-dark-950">
    <div class="pointer-events-none fixed inset-0 bg-mesh-gradient" />

    <div class="relative flex min-h-screen items-center justify-center p-6">
      <div class="w-full max-w-lg">
        <div class="mb-6 flex flex-col items-center gap-3">
          <div
            class="flex h-12 w-12 items-center justify-center rounded-2xl bg-gradient-primary text-base font-bold text-white shadow-glow"
          >
            SG
          </div>
          <div class="text-center">
            <h1 class="text-lg font-semibold text-gray-900 dark:text-white">欢迎使用 Sub2API Guardian</h1>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
              两步完成初始化，之后所有配置都会保存在本机数据库里
            </p>
          </div>
        </div>

        <div class="card">
          <div class="card-header flex items-center gap-3">
            <div
              v-for="item in steps"
              :key="item.index"
              class="flex items-center gap-2 text-sm"
              :class="
                step === item.index
                  ? 'font-medium text-primary-600 dark:text-primary-400'
                  : 'text-gray-400 dark:text-dark-500'
              "
            >
              <span
                class="flex h-6 w-6 items-center justify-center rounded-full text-xs"
                :class="
                  step > item.index
                    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
                    : step === item.index
                      ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
                      : 'bg-gray-100 text-gray-400 dark:bg-dark-800 dark:text-dark-500'
                "
              >
                <Icon v-if="step > item.index" name="check" size="xs" />
                <template v-else>{{ item.index }}</template>
              </span>
              {{ item.label }}
            </div>
          </div>

          <!-- 第 1 步：管理员账号 -->
          <form v-if="step === 1" class="space-y-4 p-6" @submit.prevent="nextStep">
            <Field v-model="form.username" label="用户名" placeholder="例如 admin" />
            <Field
              v-model="form.password"
              label="密码"
              type="password"
              :hint="`至少 ${MIN_PASSWORD} 位。忘记密码只能在服务器上重置，请妥善保存`"
            />
            <Field v-model="form.confirm" label="确认密码" type="password" />

            <p v-if="message" class="input-hint text-red-600 dark:text-red-400">{{ message }}</p>

            <button type="submit" class="btn btn-primary w-full justify-center">
              下一步
              <Icon name="arrowRight" size="sm" />
            </button>
          </form>

          <!-- 第 2 步：sub2api 连接 -->
          <form v-else class="space-y-4 p-6" @submit.prevent="submit">
            <Field
              v-model="form.baseURL"
              label="sub2api 地址"
              placeholder="http://127.0.0.1:8080"
              hint="Guardian 通过它读取分组与账号，并写回调度参数"
            />
            <Field
              v-model="form.adminKey"
              label="Admin API Key"
              type="password"
              hint="sub2api 后台「系统设置 → 管理端 API Key」"
            />
            <Field v-model="form.timeout" label="请求超时" type="number" suffix="秒" :min="5" />

            <p
              v-if="message"
              class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
            >
              <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
              <span>{{ message }}</span>
            </p>

            <div class="flex items-center gap-2">
              <button type="button" class="btn btn-ghost" :disabled="busy" @click="step = 1">
                <Icon name="arrowLeft" size="sm" />
                上一步
              </button>
              <button type="submit" class="btn btn-primary flex-1 justify-center" :disabled="busy">
                <Icon name="check" size="sm" />
                {{ busy ? '正在连接 sub2api…' : '完成初始化' }}
              </button>
            </div>
            <p class="input-hint">
              提交时会真的连一次 sub2api 校验 Key，连不上会停在这一步告诉你原因。
            </p>
          </form>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

/** 与后端 auth.MinPasswordLength 保持一致。 */
const MIN_PASSWORD = 8

const auth = useAuthStore()
const ui = useUIStore()

const steps = [
  { index: 1, label: '创建管理员账号' },
  { index: 2, label: '连接 sub2api' }
]

const step = ref(1)
const busy = ref(false)
const message = ref('')

const form = reactive({
  username: '',
  password: '',
  confirm: '',
  baseURL: 'http://127.0.0.1:8080',
  adminKey: '',
  timeout: 60
})

function nextStep() {
  message.value = ''
  if (!form.username.trim()) {
    message.value = '请填写用户名'
    return
  }
  if (form.password.length < MIN_PASSWORD) {
    message.value = `密码至少 ${MIN_PASSWORD} 位`
    return
  }
  if (form.password !== form.confirm) {
    message.value = '两次输入的密码不一致'
    return
  }
  step.value = 2
}

async function submit() {
  message.value = ''
  if (!form.baseURL.trim() || !form.adminKey.trim()) {
    message.value = '请填写 sub2api 地址与 Admin API Key'
    return
  }

  busy.value = true
  try {
    const result = await api.setup({
      username: form.username.trim(),
      password: form.password,
      base_url: form.baseURL.trim(),
      admin_api_key: form.adminKey.trim(),
      timeout_seconds: Number(form.timeout) || 60
    })

    // 连接在后端已经验过了，能走到这里说明 Key 是对的。
    // sync_error 只可能是验证之后、首次同步之前上游抖了一下，
    // 提示一句即可，后台每 2 分钟会自己重试。
    if (result.sync_error) {
      ui.notify('warning', `已完成初始化，但首次同步失败：${result.sync_error}`)
    } else {
      ui.notify('success', '初始化完成，已同步分组与渠道')
    }
    auth.completeSetup(result.username)
  } catch (err) {
    message.value = (err as Error).message
  } finally {
    busy.value = false
  }
}
</script>
