<template>
  <div class="min-h-screen bg-gray-50 dark:bg-dark-950">
    <div class="pointer-events-none fixed inset-0 bg-mesh-gradient" />

    <AppSidebar :mobile-open="mobileOpen" @navigate="mobileOpen = false" />

    <div
      v-if="mobileOpen"
      class="fixed inset-0 z-30 bg-black/40 lg:hidden"
      @click="mobileOpen = false"
    />

    <div
      class="relative min-h-screen transition-all duration-300"
      :class="ui.sidebarCollapsed ? 'lg:ml-[72px]' : 'lg:ml-64'"
    >
      <AppHeader :title="title" :subtitle="subtitle" @toggle-mobile="mobileOpen = !mobileOpen" />

      <main class="space-y-6 p-4 md:p-6 lg:p-8">
        <div
          v-if="!guardian.configured"
          class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200"
        >
          <span class="flex items-center gap-2">
            <Icon name="exclamationTriangle" size="sm" />
            尚未配置 sub2api 地址或 Admin API Key，守护流程无法启动。
          </span>
          <RouterLink to="/connection" class="btn btn-warning btn-sm">前往连接设置</RouterLink>
        </div>

        <div
          v-else-if="!guardian.monitoringEnabled"
          class="flex items-center gap-2 rounded-xl border border-primary-200 bg-primary-50 px-4 py-3 text-sm text-primary-800 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-200"
        >
          <Icon name="infoCircle" size="sm" />
          sub2api 运维监控未开启，健康分当前只使用主动探测样本；开启后可接入真实流量的错误率与延迟。
        </div>

        <div
          v-if="guardian.error"
          class="flex items-center gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
        >
          <Icon name="xCircle" size="sm" />
          {{ guardian.error }}
        </div>

        <slot />
      </main>
    </div>

    <Toasts />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppSidebar from './AppSidebar.vue'
import AppHeader from './AppHeader.vue'
import Toasts from './Toasts.vue'
import Icon from './Icon.vue'
import { useUIStore } from '@/stores/ui'
import { useGuardianStore } from '@/stores/guardian'

defineProps<{ title: string; subtitle: string }>()

const ui = useUIStore()
const guardian = useGuardianStore()
const mobileOpen = ref(false)
</script>
