<template>
  <div class="flex items-center gap-1">
    <span
      v-for="(item, index) in cells"
      :key="index"
      class="h-4 w-2 rounded-sm transition-colors"
      :class="item.cls"
      :title="item.title"
    />
    <span v-if="!samples.length" class="text-xs text-gray-400 dark:text-dark-500">暂无样本</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Sample } from '@/lib/types'
import { eventLabel } from '@/lib/format'

const props = withDefaults(defineProps<{ samples: Sample[]; limit?: number }>(), { limit: 10 })

/** 最近结果按「最新在右」展示，与人的阅读直觉一致。 */
const cells = computed(() =>
  props.samples
    .slice(0, props.limit)
    .slice()
    .reverse()
    .map(sample => ({
      cls: colorFor(sample.event_type),
      title: `${new Date(sample.occurred_at).toLocaleString()} · ${eventLabel(sample.event_type)} · ${sample.score} 分${
        sample.ttfb_ms ? ` · 首字 ${sample.ttfb_ms}ms` : ''
      }${sample.source === 'traffic' ? ' · 真实流量' : ' · 探针'}`
    }))
)

function colorFor(event: string): string {
  switch (event) {
    case 'perfect':
      return 'bg-emerald-500'
    case 'slow_ttfb':
      return 'bg-lime-500'
    case 'upstream_unknown':
      return 'bg-amber-500'
    case 'gateway_error':
      return 'bg-orange-500'
    case 'probe_fail':
      return 'bg-red-500'
    case 'fatal':
      return 'bg-red-700'
    default:
      return 'bg-gray-300 dark:bg-dark-600'
  }
}
</script>
