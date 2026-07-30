<template>
  <div class="flex items-center gap-2">
    <div class="relative h-9 w-9 flex-shrink-0">
      <svg viewBox="0 0 36 36" class="h-9 w-9 -rotate-90">
        <circle
          cx="18"
          cy="18"
          r="15.5"
          fill="none"
          stroke-width="3"
          class="stroke-gray-200 dark:stroke-dark-700"
        />
        <circle
          cx="18"
          cy="18"
          r="15.5"
          fill="none"
          stroke-width="3"
          stroke-linecap="round"
          :class="strokeClass"
          :stroke-dasharray="circumference"
          :stroke-dashoffset="offset"
        />
      </svg>
      <span
        class="absolute inset-0 flex items-center justify-center text-[11px] font-semibold"
        :class="textClass"
      >
        {{ hasSamples ? Math.round(score) : '—' }}
      </span>
    </div>
    <div v-if="showDetail" class="min-w-0 text-xs leading-tight text-gray-500 dark:text-dark-400">
      <p>短期 {{ short.toFixed(0) }}</p>
      <p>长期 {{ long.toFixed(0) }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    score: number
    short?: number
    long?: number
    sampleCount?: number
    showDetail?: boolean
  }>(),
  { short: 0, long: 0, sampleCount: 0, showDetail: false }
)

const circumference = 2 * Math.PI * 15.5
const hasSamples = computed(() => props.sampleCount > 0)
const offset = computed(() => {
  const ratio = hasSamples.value ? Math.min(Math.max(props.score, 0), 100) / 100 : 0
  return circumference * (1 - ratio)
})

const tone = computed(() => {
  if (!hasSamples.value) return 'muted'
  if (props.score >= 85) return 'success'
  if (props.score >= 60) return 'warning'
  return 'danger'
})

const strokeClass = computed(
  () =>
    ({
      success: 'stroke-emerald-500',
      warning: 'stroke-amber-500',
      danger: 'stroke-red-500',
      muted: 'stroke-gray-300 dark:stroke-dark-600'
    })[tone.value]
)

const textClass = computed(
  () =>
    ({
      success: 'text-emerald-600 dark:text-emerald-400',
      warning: 'text-amber-600 dark:text-amber-400',
      danger: 'text-red-600 dark:text-red-400',
      muted: 'text-gray-400 dark:text-dark-500'
    })[tone.value]
)
</script>
