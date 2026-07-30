<template>
  <AppLayout title="信息修改" subtitle="修改登录用户名与密码">
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
      <div class="card lg:col-span-2">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">账户信息</h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            用户名与密码可以一起改，也可以只改其中一项；两者都需要先验证当前密码。
          </p>
        </div>

        <form class="space-y-4 p-6" @submit.prevent="submit">
          <Field
            v-model="form.username"
            label="用户名"
            :hint="`当前为 ${auth.username}。留空或不修改则保持不变`"
          />

          <div class="border-t border-gray-100 pt-4 dark:border-dark-800">
            <p class="mb-3 text-sm font-medium text-gray-900 dark:text-white">修改密码（可选）</p>
            <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field
                v-model="form.newPassword"
                label="新密码"
                type="password"
                :hint="`至少 ${MIN_PASSWORD} 位，留空则不改密码`"
              />
              <Field v-model="form.confirm" label="确认新密码" type="password" />
            </div>
          </div>

          <div class="border-t border-gray-100 pt-4 dark:border-dark-800">
            <Field
              v-model="form.currentPassword"
              label="当前密码"
              type="password"
              hint="用于确认是你本人在操作 —— 只凭登录态就能改密码的话，一台没锁屏的机器就等于账号被接管"
            />
          </div>

          <p
            v-if="message"
            class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
          >
            <Icon name="xCircle" size="sm" class="mt-0.5 flex-shrink-0" />
            <span>{{ message }}</span>
          </p>

          <div class="flex items-center gap-2">
            <button type="submit" class="btn btn-primary" :disabled="busy">
              <Icon name="check" size="sm" />
              {{ busy ? '保存中…' : '保存修改' }}
            </button>
            <button type="button" class="btn btn-ghost" :disabled="busy" @click="reset">
              放弃修改
            </button>
          </div>
        </form>
      </div>

      <div class="card h-fit">
        <div class="card-header">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">当前会话</h2>
        </div>
        <div class="space-y-4 p-6">
          <div class="flex items-center gap-3">
            <div
              class="flex h-10 w-10 items-center justify-center rounded-xl bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400"
            >
              <Icon name="userCircle" size="sm" />
            </div>
            <div class="min-w-0">
              <p class="truncate text-sm font-medium text-gray-900 dark:text-white">
                {{ auth.username }}
              </p>
              <p class="text-xs text-gray-500 dark:text-dark-400">已登录</p>
            </div>
          </div>

          <p class="text-xs text-gray-500 dark:text-dark-400">
            改密码后，其他设备上的登录状态会被一并注销，只保留当前这一个。
          </p>
          <p class="text-xs text-gray-500 dark:text-dark-400">
            退出登录在右上角。退出只结束浏览器这一侧的会话，
            <span class="text-gray-700 dark:text-dark-200">后台调度不会停止</span>，
            熔断、回池与写回照常按策略进行。
          </p>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

/** 与后端 auth.MinPasswordLength 保持一致。 */
const MIN_PASSWORD = 8

const auth = useAuthStore()
const ui = useUIStore()

const busy = ref(false)
const message = ref('')

const form = reactive({
  username: auth.username,
  currentPassword: '',
  newPassword: '',
  confirm: ''
})

function reset() {
  form.username = auth.username
  form.currentPassword = ''
  form.newPassword = ''
  form.confirm = ''
  message.value = ''
}

async function submit() {
  message.value = ''

  const nameChanged = form.username.trim() !== '' && form.username.trim() !== auth.username
  const passwordChanged = form.newPassword !== ''

  if (!nameChanged && !passwordChanged) {
    message.value = '没有任何改动'
    return
  }
  if (passwordChanged) {
    if (form.newPassword.length < MIN_PASSWORD) {
      message.value = `新密码至少 ${MIN_PASSWORD} 位`
      return
    }
    if (form.newPassword !== form.confirm) {
      message.value = '两次输入的新密码不一致'
      return
    }
  }
  if (!form.currentPassword) {
    message.value = '请填写当前密码'
    return
  }

  busy.value = true
  try {
    const payload: { current_password: string; username?: string; new_password?: string } = {
      current_password: form.currentPassword
    }
    if (nameChanged) payload.username = form.username.trim()
    if (passwordChanged) payload.new_password = form.newPassword

    const result = await api.updateAccount(payload)
    auth.username = result.username
    ui.notify('success', `已更新：${result.changed.join('、')}`)
    reset()
  } catch (err) {
    message.value = (err as Error).message
  } finally {
    busy.value = false
  }
}
</script>
