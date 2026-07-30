<template>
  <AppLayout title="事件日志" subtitle="同步、探测、熔断、保底、回池与写回的完整审计轨迹">
    <div class="card">
      <div class="card-header flex flex-wrap items-center gap-3">
        <SegmentedControl
          :model-value="level"
          :options="[
            { value: '', label: '全部' },
            { value: 'info', label: '信息' },
            { value: 'warn', label: '警告' },
            { value: 'error', label: '错误' }
          ]"
          @update:model-value="setLevel"
        />
        <select v-model="groupID" class="input w-48" @change="load(1)">
          <option value="">全部分组</option>
          <option v-for="group in guardian.groups" :key="group.id" :value="String(group.id)">
            {{ group.name }}
          </option>
        </select>
        <div class="flex-1" />
        <span class="text-sm text-gray-500 dark:text-dark-400">共 {{ total }} 条</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load(page)">
          <Icon name="refresh" size="sm" />
          刷新
        </button>
      </div>

      <div class="p-4">
        <EmptyState v-if="!items.length" icon="document" title="暂无事件" description="调整筛选条件试试。" />
        <ul v-else class="space-y-2">
          <li
            v-for="event in items"
            :key="event.id"
            class="rounded-xl border p-3 transition-colors"
            :class="
              isCleanupEvent(event)
                ? 'border-red-200 bg-red-50/60 dark:border-red-900/60 dark:bg-red-900/10'
                : 'border-gray-100 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800/40'
            "
          >
            <div class="flex flex-wrap items-start gap-3">
              <Badge :tone="levelMeta(event.level).tone" dot>{{ levelMeta(event.level).label }}</Badge>
              <div class="min-w-0 flex-1">
                <p class="text-sm text-gray-900 dark:text-white">{{ event.message }}</p>
                <p class="mt-1 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
                  <span class="code">{{ event.action }}</span>
                  <span v-if="event.account_id">渠道 #{{ event.account_id }}</span>
                  <span v-if="event.group_id">分组 #{{ event.group_id }}</span>
                  <span>{{ formatTime(event.created_at) }}</span>
                </p>
              </div>
              <button
                v-if="event.detail"
                type="button"
                class="btn btn-ghost btn-sm"
                @click="toggle(event.id)"
              >
                {{ expanded.has(event.id) ? '收起' : '详情' }}
              </button>
            </div>
            <pre
              v-if="event.detail && expanded.has(event.id)"
              class="code-block mt-3 max-h-64 overflow-auto whitespace-pre-wrap text-xs"
              >{{ prettify(event.detail) }}</pre
            >
          </li>
        </ul>
      </div>

      <div v-if="pages > 1" class="card-footer flex items-center justify-between">
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="page <= 1 || loading"
          @click="load(page - 1)"
        >
          <Icon name="chevronLeft" size="sm" />
          上一页
        </button>
        <span class="text-sm text-gray-500 dark:text-dark-400">第 {{ page }} / {{ pages }} 页</span>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="page >= pages || loading"
          @click="load(page + 1)"
        >
          下一页
          <Icon name="chevronRight" size="sm" />
        </button>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import Badge from '@/components/Badge.vue'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { useGuardianStore } from '@/stores/guardian'
import { useUIStore } from '@/stores/ui'
import { api } from '@/lib/api'
import { formatTime, levelMeta } from '@/lib/format'
import type { GuardianEvent } from '@/lib/types'

const guardian = useGuardianStore()
const ui = useUIStore()

const items = ref<GuardianEvent[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 30
const level = ref('')
const groupID = ref('')
const loading = ref(false)
const expanded = ref(new Set<number>())

const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))

onMounted(() => load(1))

function setLevel(value: string) {
  level.value = value
  void load(1)
}

async function load(target: number) {
  loading.value = true
  try {
    const params: Record<string, string> = {
      page: String(Math.max(1, target)),
      page_size: String(pageSize)
    }
    if (level.value) params.level = level.value
    if (groupID.value) params.group_id = groupID.value

    const data = await api.events(params)
    items.value = data.items ?? []
    total.value = data.total
    page.value = data.page
  } catch (err) {
    ui.notify('error', (err as Error).message)
  } finally {
    loading.value = false
  }
}

function toggle(id: number) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

// isCleanupEvent 标记自动处置类事件：删除不可逆，不该淹没在普通日志里。
function isCleanupEvent(event: GuardianEvent): boolean {
  return event.action.startsWith('cleanup_')
}

function prettify(detail: string): string {
  try {
    return JSON.stringify(JSON.parse(detail), null, 2)
  } catch {
    return detail
  }
}
</script>
