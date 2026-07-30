<template>
  <div class="relative min-h-screen bg-gray-50 dark:bg-dark-950">
    <div class="pointer-events-none fixed inset-0 bg-mesh-gradient" />

    <div class="relative flex min-h-screen items-center justify-center p-6">
      <div class="w-full max-w-sm">
        <div class="mb-6 flex flex-col items-center gap-3">
          <div
            class="flex h-12 w-12 items-center justify-center rounded-2xl bg-gradient-primary text-base font-bold text-white shadow-glow"
          >
            SG
          </div>
          <div class="text-center">
            <h1 class="text-lg font-semibold text-gray-900 dark:text-white">Sub2API Guardian</h1>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">渠道调度守护</p>
          </div>
        </div>

        <form class="card p-6" @submit.prevent="submit">
          <div class="space-y-4">
            <Field v-model="username" label="用户名" placeholder="管理员用户名" />
            <Field v-model="password" label="密码" type="password" placeholder="登录密码" />

            <p
              v-if="message"
              class="flex items-center gap-2 rounded-xl border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
            >
              <Icon name="xCircle" size="sm" />
              {{ message }}
            </p>

            <button type="submit" class="btn btn-primary w-full justify-center" :disabled="auth.busy">
              <Icon name="login" size="sm" />
              {{ auth.busy ? '登录中…' : '登录' }}
            </button>
          </div>
        </form>

        <p class="mt-4 text-center text-xs text-gray-400 dark:text-dark-500">
          忘记密码需要在服务器上重置，详见 README「忘记密码」一节
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import Field from '@/components/Field.vue'
import Icon from '@/components/Icon.vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const username = ref('')
const password = ref('')
const message = ref('')

async function submit() {
  message.value = ''
  if (!username.value.trim() || !password.value) {
    message.value = '请填写用户名与密码'
    return
  }
  try {
    await auth.login(username.value.trim(), password.value)
  } catch (err) {
    message.value = (err as Error).message
    password.value = ''
  }
}
</script>
