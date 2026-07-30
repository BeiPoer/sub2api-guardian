<template>
  <div class="stat-card card-hover">
    <div :class="['stat-icon', iconClass]">
      <Icon :name="icon" size="lg" />
    </div>
    <div class="min-w-0 flex-1">
      <p class="stat-label truncate">{{ label }}</p>
      <p class="stat-value" :title="String(display)">{{ display }}</p>
      <p v-if="meta" class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400">{{ meta }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from './Icon.vue'
import type { IconName } from '@/lib/icons'

const props = withDefaults(
  defineProps<{
    label: string
    value: number | string
    meta?: string
    tone?: string
    icon?: IconName
    decimals?: number
  }>(),
  { tone: 'primary', icon: 'chartBar', decimals: 0 }
)

const display = computed(() => {
  if (typeof props.value === 'string') return props.value
  return props.value.toLocaleString(undefined, {
    minimumFractionDigits: props.decimals,
    maximumFractionDigits: props.decimals
  })
})

const iconClass = computed(() => {
  const map: Record<string, string> = {
    primary: 'stat-icon-primary',
    teal: 'stat-icon-primary',
    success: 'stat-icon-success',
    warning: 'stat-icon-warning',
    danger: 'stat-icon-danger'
  }
  return map[props.tone] ?? 'stat-icon-primary'
})
</script>
