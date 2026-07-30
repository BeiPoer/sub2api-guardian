<template>
  <div
    class="flex items-start justify-between gap-4 rounded-xl border border-gray-100 bg-gray-50/60 px-4 py-3 dark:border-dark-700 dark:bg-dark-900/40"
  >
    <div class="min-w-0">
      <p class="text-sm font-medium text-gray-900 dark:text-white">{{ label }}</p>
      <p v-if="description" class="mt-0.5 text-xs leading-relaxed text-gray-500 dark:text-dark-400">
        {{ description }}
      </p>
    </div>
    <Toggle :model-value="modelValue" :disabled="disabled" @update:model-value="emit('update:modelValue', $event)" />
  </div>
</template>

<script setup lang="ts">
import Toggle from './Toggle.vue'

defineProps<{
  label: string
  description?: string
  modelValue: boolean
  disabled?: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()
</script>
