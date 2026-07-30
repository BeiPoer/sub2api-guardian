<template>
  <div class="empty-state">
    <Icon :name="icon" class="empty-state-icon" />
    <p class="empty-state-title">{{ title }}</p>
    <p v-if="description" class="empty-state-description">{{ description }}</p>
    <slot />
  </div>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'
import type { IconName } from '@/lib/icons'

withDefaults(
  defineProps<{
    title: string
    description?: string
    icon?: IconName
  }>(),
  { icon: 'inbox' }
)
</script>
