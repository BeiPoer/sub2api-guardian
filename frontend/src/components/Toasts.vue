<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed right-4 top-4 z-[100] flex w-full max-w-sm flex-col gap-3">
      <TransitionGroup name="toast">
        <div
          v-for="toast in ui.toasts"
          :key="toast.id"
          class="pointer-events-auto rounded-xl border-l-4 bg-white p-4 shadow-lg dark:bg-dark-800"
          :class="borderFor(toast.kind)"
        >
          <div class="flex items-start gap-3">
            <Icon :name="iconFor(toast.kind)" size="sm" :class="colorFor(toast.kind)" />
            <p class="flex-1 text-sm text-gray-700 dark:text-gray-200">{{ toast.message }}</p>
            <button type="button" class="text-gray-400 hover:text-gray-600" @click="ui.dismiss(toast.id)">
              <Icon name="x" size="xs" />
            </button>
          </div>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'
import { useUIStore, type ToastKind } from '@/stores/ui'

const ui = useUIStore()

function borderFor(kind: ToastKind) {
  return {
    success: 'border-l-emerald-500',
    error: 'border-l-red-500',
    warning: 'border-l-amber-500',
    info: 'border-l-primary-500'
  }[kind]
}

function colorFor(kind: ToastKind) {
  return {
    success: 'text-emerald-500',
    error: 'text-red-500',
    warning: 'text-amber-500',
    info: 'text-primary-500'
  }[kind]
}

function iconFor(kind: ToastKind) {
  return {
    success: 'checkCircle',
    error: 'xCircle',
    warning: 'exclamationTriangle',
    info: 'infoCircle'
  }[kind] as 'checkCircle' | 'xCircle' | 'exclamationTriangle' | 'infoCircle'
}
</script>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: all 0.25s ease;
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateX(20px);
}
</style>
