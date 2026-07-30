<template>
  <div
    v-if="totalPages > 1"
    class="flex flex-wrap items-center justify-between gap-2 border-t border-gray-100 pt-3 dark:border-dark-700"
  >
    <span class="text-xs text-gray-500 dark:text-dark-400">
      第 {{ rangeStart }}–{{ rangeEnd }} 个，共 {{ total }} 个
    </span>

    <div class="flex items-center gap-1">
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        :disabled="page <= 1"
        title="上一页"
        @click="emit('update:page', page - 1)"
      >
        <Icon name="chevronLeft" size="xs" />
      </button>
      <span class="px-1 text-xs tabular-nums text-gray-600 dark:text-dark-300">
        {{ page }} / {{ totalPages }}
      </span>
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        :disabled="page >= totalPages"
        title="下一页"
        @click="emit('update:page', page + 1)"
      >
        <Icon name="chevronRight" size="xs" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from './Icon.vue'

const props = defineProps<{
  page: number
  pageSize: number
  total: number
}>()

const emit = defineEmits<{ (e: 'update:page', value: number): void }>()

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
const rangeStart = computed(() => (props.page - 1) * props.pageSize + 1)
const rangeEnd = computed(() => Math.min(props.page * props.pageSize, props.total))
</script>
