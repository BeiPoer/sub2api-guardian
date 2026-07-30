<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="open" class="modal-overlay" @click.self="emit('close')">
        <div class="modal-content max-w-2xl" @click.stop>
          <div class="modal-header">
            <div>
              <h2 class="modal-title">{{ title }}</h2>
              <p v-if="subtitle" class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">{{ subtitle }}</p>
            </div>
            <button type="button" class="btn btn-ghost btn-icon" @click="emit('close')">
              <Icon name="x" size="sm" />
            </button>
          </div>
          <div class="modal-body">
            <slot />
          </div>
          <div v-if="$slots.footer" class="modal-footer">
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'

defineProps<{ open: boolean; title: string; subtitle?: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()
</script>
