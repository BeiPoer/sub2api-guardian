<template>
  <div class="tabs w-fit">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      class="tab whitespace-nowrap"
      :class="modelValue === option.value && 'tab-active'"
      @click="emit('update:modelValue', option.value)"
    >
      <span class="flex items-center gap-1.5">
        <Icon v-if="option.icon" :name="option.icon" size="sm" />
        {{ option.label }}
      </span>
    </button>
  </div>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'
import type { IconName } from '@/lib/icons'

interface Option {
  value: string
  label: string
  icon?: IconName
}

defineProps<{ modelValue: string; options: Option[] }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()
</script>
