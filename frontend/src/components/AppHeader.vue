<template>
  <header
    class="glass sticky top-0 z-30 flex h-16 items-center gap-3 border-b border-gray-200 px-4 dark:border-dark-800 md:px-6"
  >
    <button type="button" class="btn btn-ghost btn-icon lg:hidden" @click="emit('toggleMobile')">
      <Icon name="menu" size="sm" />
    </button>

    <div class="min-w-0 flex-1">
      <h1 class="truncate text-lg font-semibold text-gray-900 dark:text-white">{{ title }}</h1>
      <p class="truncate text-xs text-gray-500 dark:text-dark-400">{{ subtitle }}</p>
    </div>

    <div class="flex items-center gap-2">
      <span
        class="hidden items-center gap-1.5 rounded-lg bg-gray-100 px-2.5 py-1.5 text-xs text-gray-600 dark:bg-dark-800 dark:text-dark-300 sm:flex"
        :title="lastRunTitle"
      >
        <span
          class="h-1.5 w-1.5 rounded-full"
          :class="guardian.status?.running ? 'animate-pulse bg-amber-500' : 'bg-emerald-500'"
        />
        {{ guardian.status?.running ? '调度中' : `上次 ${formatRelative(guardian.status?.last_run_at)}` }}
      </span>

      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="guardian.busy"
        @click="sync"
      >
        <Icon name="sync" size="sm" />
        <span class="hidden sm:inline">同步</span>
      </button>
      <!--
        「跑一轮」与「启动/取消调度」是两回事，文案上要能看出来：
          跑一轮   —— 立刻执行一次，跑完就完，不改变自动守护开关
          取消调度 —— 关掉自动守护，心跳不再发起新轮次（持久化、重启保持）
        早期两个按钮都叫「调度」，看不出区别。
      -->
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="guardian.busy || guardian.status?.running"
        title="立刻执行一轮：探测、判定、写回走一遍。跑完即止，不改变下方的自动守护开关"
        @click="runOnce"
      >
        <Icon name="play" size="sm" />
        <span class="hidden sm:inline">
          {{ guardian.status?.running ? '执行中…' : '跑一轮' }}
        </span>
      </button>

      <!--
        自动守护的开关按钮。状态取自 auto_enabled（持久化在连接配置里），
        而不是「当前有没有轮次在跑」—— 后者会让按钮在两轮之间来回跳。
      -->
      <button
        v-if="guardian.autoEnabled"
        type="button"
        class="btn btn-danger btn-sm"
        :disabled="toggling"
        title="关闭自动守护：中断当前轮次，之后心跳不再自动发起新轮次（重启后保持关闭）"
        @click="stopScheduling"
      >
        <Icon name="pause" size="sm" />
        <span class="hidden sm:inline">{{ toggling ? '正在停止…' : '取消调度' }}</span>
      </button>
      <button
        v-else
        type="button"
        class="btn btn-primary btn-sm"
        :disabled="toggling"
        title="开启自动守护：每 15 秒心跳自动执行调度"
        @click="startScheduling"
      >
        <Icon name="play" size="sm" />
        <span class="hidden sm:inline">{{ toggling ? '正在启动…' : '启动调度' }}</span>
      </button>
      <button type="button" class="btn btn-ghost btn-icon" :title="ui.dark ? '切换浅色' : '切换深色'" @click="ui.toggleTheme()">
        <Icon :name="ui.dark ? 'sun' : 'moon'" size="sm" />
      </button>

      <!--
        退出登录只结束浏览器这一侧的会话。调度引擎跑在服务端进程里，
        与谁登录着无关：退出后自动守护照常继续，下次登录能看到期间的全部事件。
      -->
      <button
        v-if="auth.username"
        type="button"
        class="btn btn-ghost btn-sm"
        :disabled="auth.busy"
        title="退出登录（后台调度不会停止，仍按策略继续运行）"
        @click="logout"
      >
        <Icon name="login" size="sm" />
        <span class="hidden sm:inline">{{ auth.busy ? '退出中…' : '退出登录' }}</span>
      </button>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import Icon from './Icon.vue'
import { useUIStore } from '@/stores/ui'
import { useGuardianStore } from '@/stores/guardian'
import { useAuthStore } from '@/stores/auth'
import { api } from '@/lib/api'
import { formatRelative, formatTime } from '@/lib/format'

defineProps<{ title: string; subtitle: string }>()
const emit = defineEmits<{ (e: 'toggleMobile'): void }>()

const ui = useUIStore()
const guardian = useGuardianStore()
const auth = useAuthStore()

const toggling = ref(false)

const lastRunTitle = computed(() => {
  const status = guardian.status
  if (!status) return ''
  const summary = status.last_summary
  return [
    `上次调度：${formatTime(status.last_run_at)}（${status.last_run_ms}ms）`,
    `渠道 ${summary.channels} · 探测 ${summary.probed} · 新样本 ${summary.samples}`,
    `熔断 ${summary.fused} · 回池 ${summary.recovered} · 写回 ${summary.applied}`,
    status.last_run_error ? `错误：${status.last_run_error}` : ''
  ]
    .filter(Boolean)
    .join('\n')
})

async function sync() {
  try {
    await guardian.run(() => api.sync())
    ui.notify('success', '已从 sub2api 同步分组与账号')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

async function runOnce() {
  try {
    await guardian.run(() => api.runOnce())
    ui.notify('success', '调度已完成')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  }
}

// logout 只结束浏览器这一侧的会话。
//
// 服务端的调度循环是独立 goroutine，不依赖任何登录态：退出后自动守护继续跑，
// 熔断、回池、写回照常发生，期间的事件下次登录都能看到。
async function logout() {
  await auth.logout()
}

// stopScheduling / startScheduling 不走 guardian.run()：那会把 busy 置真，
// 而「立即调度」本身可能还在飞行中，两者会互相干扰按钮状态。
async function stopScheduling() {
  toggling.value = true
  try {
    const result = await api.cancelRun()
    ui.notify(
      'success',
      result.canceled
        ? '已停止自动调度，并中断了当前轮次与进行中的测试'
        : '已停止自动调度（当时没有正在执行的轮次）'
    )
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    toggling.value = false
    void guardian.refresh({ silent: true })
  }
}

async function startScheduling() {
  toggling.value = true
  try {
    await api.resumeRun()
    ui.notify('success', '已启动自动调度')
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    toggling.value = false
    void guardian.refresh({ silent: true })
  }
}
</script>
