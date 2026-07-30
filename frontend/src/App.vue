<template>
  <!-- 探测登录态期间先不渲染，避免登录页一闪而过 -->
  <div
    v-if="auth.state === 'loading'"
    class="flex min-h-screen items-center justify-center bg-gray-50 dark:bg-dark-950"
  >
    <div class="flex items-center gap-3 text-sm text-gray-500 dark:text-dark-400">
      <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      正在加载…
    </div>
  </div>

  <SetupView v-else-if="auth.state === 'setup'" />
  <LoginView v-else-if="auth.state === 'login'" />
  <RouterView v-else />
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, watch } from 'vue'
import { RouterView } from 'vue-router'
import LoginView from '@/views/LoginView.vue'
import SetupView from '@/views/SetupView.vue'
import { useAuthStore } from '@/stores/auth'
import { useGuardianStore } from '@/stores/guardian'

const auth = useAuthStore()
const guardian = useGuardianStore()

onMounted(() => {
  void auth.bootstrap()
})

// 只有登录之后才拉数据、连 SSE。
//
// 未登录就拉的话每个接口都会 401：控制台一片红，SSE 还会不停重连。
watch(
  () => auth.state,
  async (state, previous) => {
    if (state === 'ready') {
      await guardian.refresh()
      guardian.connect()
      return
    }
    if (previous === 'ready') {
      // 退出登录或会话过期：断开推送并清掉内存里的数据，
      // 否则下一个登录进来的人会先看到上一个人的渠道列表。
      guardian.disconnect()
      guardian.reset()
    }
  }
)

onBeforeUnmount(() => guardian.disconnect())
</script>
