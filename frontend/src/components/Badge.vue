<template>
  <span :class="['badge', toneClass]">
    <span v-if="dot" class="h-1.5 w-1.5 rounded-full bg-current" />
    <slot>{{ label }}</slot>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{ label?: string; tone?: string; dot?: boolean }>(),
  { tone: 'gray' }
)

const toneClass = computed(() => {
  const map: Record<string, string> = {
    primary: 'badge-primary',
    success: 'badge-success',
    warning: 'badge-warning',
    danger: 'badge-danger',
    purple: 'badge-purple',
    gray: 'badge-gray'
  }
  return map[props.tone] ?? 'badge-gray'
})
</script>
