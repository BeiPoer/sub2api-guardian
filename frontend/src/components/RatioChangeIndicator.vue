<template>
  <span v-if="change" class="inline-flex flex-shrink-0" :title="title" :aria-label="title">
    <Icon
      :name="change.after > change.before ? 'arrowUp' : 'arrowDown'"
      size="xs"
      :stroke-width="2.5"
      :class="change.after > change.before ? 'text-red-500 dark:text-red-400' : 'text-emerald-500 dark:text-emerald-400'"
    />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/Icon.vue'
import { formatTime } from '@/lib/format'
import type { UpstreamGroupRatioChange } from '@/lib/types'

const props = defineProps<{ change?: UpstreamGroupRatioChange }>()

const title = computed(() => props.change
  ? `${formatTime(props.change.changed_at)}从${formatRatio(props.change.before)}变更为${formatRatio(props.change.after)}`
  : '')

function formatRatio(value: number) {
  const formatted = Number.isInteger(value) ? String(value) : value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
  return `${formatted}x`
}
</script>
