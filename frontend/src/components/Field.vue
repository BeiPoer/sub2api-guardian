<template>
  <label class="block">
    <span class="input-label flex items-center gap-1.5">
      {{ label }}
      <span v-if="suffix" class="text-xs font-normal text-gray-400 dark:text-dark-500">{{ suffix }}</span>
    </span>
    <input
      class="input"
      :type="type"
      :value="modelValue"
      :min="min"
      :max="max"
      :step="step"
      :placeholder="placeholder"
      :disabled="disabled"
      @input="onInput"
    />
    <span v-if="hint" class="input-hint">{{ hint }}</span>
  </label>
</template>

<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    label: string
    modelValue: string | number
    type?: 'text' | 'number' | 'password'
    hint?: string
    suffix?: string
    placeholder?: string
    min?: number
    max?: number
    step?: number
    disabled?: boolean
  }>(),
  { type: 'text', step: 1 }
)

const emit = defineEmits<{ (e: 'update:modelValue', value: string | number): void }>()

function onInput(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  if (props.type !== 'number') {
    emit('update:modelValue', raw)
    return
  }
  // 数字框允许暂时为空，交给后端的规范化兜底，避免输入过程被打断。
  const parsed = raw === '' ? 0 : Number(raw)
  emit('update:modelValue', Number.isNaN(parsed) ? 0 : parsed)
}
</script>
