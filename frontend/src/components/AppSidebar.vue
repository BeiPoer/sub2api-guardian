<template>
  <aside
    class="sidebar"
    :class="[ui.sidebarCollapsed ? 'w-[72px]' : 'w-64', mobileOpen ? 'translate-x-0' : '-translate-x-full lg:translate-x-0']"
  >
    <div class="sidebar-header" :class="ui.sidebarCollapsed && 'justify-center px-0'">
      <div
        class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-xl bg-gradient-primary text-sm font-bold text-white shadow-glow"
      >
        SG
      </div>
      <div v-if="!ui.sidebarCollapsed" class="min-w-0">
        <p class="truncate text-sm font-semibold text-gray-900 dark:text-white">Sub2API Guardian</p>
        <p class="truncate text-xs text-gray-500 dark:text-dark-400">渠道调度守护</p>
      </div>
    </div>

    <nav class="sidebar-nav">
      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">监控</p>
        <RouterLink
          v-for="item in monitorNav"
          :key="item.to"
          :to="item.to"
          class="sidebar-link mb-1"
          :class="isActive(item.to) && 'sidebar-link-active'"
          :title="item.label"
          @click="emit('navigate')"
        >
          <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
          <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
        </RouterLink>
      </div>

      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">配置</p>
        <RouterLink
          v-for="item in configNav"
          :key="item.to"
          :to="item.to"
          class="sidebar-link mb-1"
          :class="isActive(item.to) && 'sidebar-link-active'"
          :title="item.label"
          @click="emit('navigate')"
        >
          <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
          <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
        </RouterLink>
      </div>

      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">定时报告</p>
        <div :class="!ui.sidebarCollapsed && 'ml-4 border-l border-gray-200 pl-2 dark:border-dark-700'">
          <RouterLink
            v-for="item in scheduledReportsNav"
            :key="item.to"
            :to="item.to"
            class="sidebar-link mb-1"
            :class="isActive(item.to) && 'sidebar-link-active'"
            :title="item.label"
            @click="emit('navigate')"
          >
            <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
            <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
          </RouterLink>
        </div>
      </div>

      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">辅助工具</p>
        <RouterLink
          v-for="item in toolsNav"
          :key="item.to"
          :to="item.to"
          class="sidebar-link mb-1"
          :class="isActive(item.to) && 'sidebar-link-active'"
          :title="item.label"
          @click="emit('navigate')"
        >
          <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
          <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
        </RouterLink>
      </div>

      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">渠道管理</p>
        <div :class="!ui.sidebarCollapsed && 'ml-4 border-l border-gray-200 pl-2 dark:border-dark-700'">
          <RouterLink
            v-for="item in channelManagementNav"
            :key="item.to"
            :to="item.to"
            class="sidebar-link mb-1"
            :class="isActive(item.to) && 'sidebar-link-active'"
            :title="item.label"
            @click="emit('navigate')"
          >
            <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
            <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
          </RouterLink>
        </div>
      </div>

      <div class="sidebar-section">
        <p v-if="!ui.sidebarCollapsed" class="sidebar-section-title">系统</p>
        <RouterLink
          v-for="item in systemNav"
          :key="item.to"
          :to="item.to"
          class="sidebar-link mb-1"
          :class="isActive(item.to) && 'sidebar-link-active'"
          :title="item.label"
          @click="emit('navigate')"
        >
          <Icon :name="item.icon" size="sm" class="flex-shrink-0" />
          <span v-if="!ui.sidebarCollapsed" class="truncate">{{ item.label }}</span>
        </RouterLink>
      </div>
    </nav>

    <div class="border-t border-gray-100 p-3 dark:border-dark-800">
      <div v-if="!ui.sidebarCollapsed" class="mb-3 space-y-1.5 px-1">
        <div class="flex items-center gap-2 text-xs">
          <span class="h-2 w-2 rounded-full" :class="guardian.autoEnabled ? 'bg-emerald-500' : 'bg-gray-400'" />
          <span class="text-gray-600 dark:text-dark-300">
            {{ guardian.autoEnabled ? '自动守护中' : '自动守护已暂停' }}
          </span>
        </div>
        <div class="flex items-center gap-2 text-xs">
          <span class="h-2 w-2 rounded-full" :class="guardian.monitoringEnabled ? 'bg-emerald-500' : 'bg-amber-500'" />
          <span class="text-gray-600 dark:text-dark-300">
            {{ guardian.monitoringEnabled ? '真实流量已接入' : '纯探针模式' }}
          </span>
        </div>
      </div>

      <!-- 只显示当前用户；退出登录移到了右上角，避免两处入口 -->
      <RouterLink
        v-if="auth.username"
        to="/account"
        class="mb-2 flex min-w-0 items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-gray-600 transition-colors hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800"
        :class="ui.sidebarCollapsed && 'justify-center px-1.5'"
        :title="`${auth.username} · 信息修改`"
        @click="emit('navigate')"
      >
        <Icon name="userCircle" size="sm" class="flex-shrink-0" />
        <span v-if="!ui.sidebarCollapsed" class="truncate">{{ auth.username }}</span>
      </RouterLink>

      <button type="button" class="btn btn-ghost w-full justify-center" @click="ui.toggleSidebar()">
        <Icon :name="ui.sidebarCollapsed ? 'chevronRight' : 'chevronLeft'" size="sm" />
        <span v-if="!ui.sidebarCollapsed">收起</span>
      </button>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { useRoute, RouterLink } from 'vue-router'
import Icon from './Icon.vue'
import { useUIStore } from '@/stores/ui'
import { useGuardianStore } from '@/stores/guardian'
import { useAuthStore } from '@/stores/auth'

defineProps<{ mobileOpen: boolean }>()
const emit = defineEmits<{ (e: 'navigate'): void }>()

const route = useRoute()
const ui = useUIStore()
const guardian = useGuardianStore()
const auth = useAuthStore()

const monitorNav = [
  { to: '/', label: '总览', icon: 'home' as const },
  { to: '/groups', label: '分组调度', icon: 'grid' as const },
  { to: '/channels', label: '渠道池', icon: 'server' as const },
  { to: '/events', label: '事件日志', icon: 'document' as const }
]

const configNav = [
  { to: '/policy', label: '策略配置', icon: 'cog' as const },
  { to: '/connection', label: '连接设置', icon: 'link' as const }
]

const scheduledReportsNav = [
  { to: '/reports/source', label: '源站配置', icon: 'globe' as const },
  { to: '/reports/notifications', label: '通知配置', icon: 'bell' as const },
  { to: '/reports/channel-usage', label: '渠道使用报告', icon: 'calendar' as const },
  { to: '/reports/daily', label: '每日报告', icon: 'chartBar' as const }
]

const toolsNav = [
  { to: '/tools/image2', label: 'image2路由', icon: 'beaker' as const },
  { to: '/tools/memos', label: '备忘录', icon: 'clipboard' as const }
]

const channelManagementNav = [
  { to: '/upstream-channels/summary', label: '渠道汇总', icon: 'chartBar' as const },
  { to: '/upstream-channels/list', label: '渠道列表', icon: 'server' as const },
  { to: '/upstream-channels/multiplier-source', label: '倍率同步源站', icon: 'link' as const }
]

/** 系统：与调度无关的账号自身设置。 */
const systemNav = [{ to: '/account', label: '信息修改', icon: 'userCircle' as const }]

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}
</script>
