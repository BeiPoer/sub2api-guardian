<template>
  <div class="memo-document-editor">
    <div ref="toolbarElement" class="memo-document-toolbar">
      <select class="ql-header" aria-label="段落样式" title="段落样式">
        <option value="1">标题 1</option>
        <option value="2">标题 2</option>
        <option selected>正文</option>
      </select>
      <span class="ql-formats">
        <button class="ql-bold" type="button" aria-label="粗体" title="粗体" />
        <button class="ql-italic" type="button" aria-label="斜体" title="斜体" />
        <button class="ql-underline" type="button" aria-label="下划线" title="下划线" />
      </span>
      <span class="ql-formats">
        <button class="ql-list" value="ordered" type="button" aria-label="有序列表" title="有序列表" />
        <button class="ql-list" value="bullet" type="button" aria-label="无序列表" title="无序列表" />
        <button class="ql-blockquote" type="button" aria-label="引用" title="引用" />
        <button class="ql-link" type="button" aria-label="链接" title="链接" />
      </span>
      <span class="ql-formats">
        <button type="button" class="memo-history-button" aria-label="撤销" title="撤销" @click="undo">
          <Icon name="arrowLeft" size="sm" />
        </button>
        <button type="button" class="memo-history-button" aria-label="重做" title="重做" @click="redo">
          <Icon name="arrowRight" size="sm" />
        </button>
      </span>
    </div>
    <div ref="editorElement" class="memo-document-surface" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Quill, { Delta } from 'quill'
import 'quill/dist/quill.snow.css'
import Icon from './Icon.vue'
import type { DocumentMemoContent } from '@/lib/types'

const props = withDefaults(
  defineProps<{
    modelValue: DocumentMemoContent
    disabled?: boolean
  }>(),
  { disabled: false }
)

const emit = defineEmits<{
  (event: 'update:modelValue', value: DocumentMemoContent): void
}>()

const toolbarElement = ref<HTMLElement | null>(null)
const editorElement = ref<HTMLElement | null>(null)
let quill: Quill | null = null
let applyingExternalValue = false

interface HistoryModule {
  clear(): void
  undo(): void
  redo(): void
}

onMounted(() => {
  if (!editorElement.value || !toolbarElement.value) return
  quill = new Quill(editorElement.value, {
    theme: 'snow',
    formats: ['header', 'bold', 'italic', 'underline', 'list', 'blockquote', 'link'],
    modules: {
      toolbar: toolbarElement.value,
      history: { delay: 800, maxStack: 100, userOnly: true }
    }
  })
  setEditorContent(props.modelValue)
  quill.enable(!props.disabled)
  quill.on('text-change', handleTextChange)
})

onBeforeUnmount(() => {
  quill?.off('text-change', handleTextChange)
  quill = null
})

watch(
  () => props.modelValue,
  value => {
    if (!quill || serialize(quill.getContents()) === serialize(value)) return
    setEditorContent(value)
  },
  { deep: true }
)

watch(
  () => props.disabled,
  disabled => quill?.enable(!disabled)
)

function setEditorContent(value: DocumentMemoContent) {
  if (!quill) return
  applyingExternalValue = true
  quill.setContents(new Delta(value.ops), 'silent')
  historyModule()?.clear()
  void nextTick(() => {
    applyingExternalValue = false
  })
}

function handleTextChange() {
  if (!quill || applyingExternalValue) return
  emit('update:modelValue', JSON.parse(JSON.stringify(quill.getContents())) as DocumentMemoContent)
}

function undo() {
  if (props.disabled || !quill) return
  historyModule()?.undo()
}

function redo() {
  if (props.disabled || !quill) return
  historyModule()?.redo()
}

function historyModule(): HistoryModule | null {
  return (quill?.getModule('history') as HistoryModule | undefined) ?? null
}

function serialize(value: unknown): string {
  return JSON.stringify(value)
}
</script>

<style scoped>
.memo-document-editor {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  background: white;
}

.memo-document-toolbar {
  flex-shrink: 0;
  border-width: 0 0 1px !important;
  border-color: rgb(229 231 235) !important;
  background: rgb(249 250 251);
}

.memo-document-surface {
  min-height: 24rem;
  flex: 1;
  overflow-y: auto;
  border: 0 !important;
  font-family: inherit;
  font-size: 15px;
}

.memo-history-button {
  align-items: center;
  justify-content: center;
  color: rgb(68 68 68);
}

.memo-history-button:hover {
  color: rgb(13 148 136);
}

:deep(.ql-editor) {
  min-height: 24rem;
  padding: 1.5rem;
  line-height: 1.7;
}

:deep(.ql-editor.ql-blank::before) {
  color: rgb(156 163 175);
  content: '开始记录…';
  font-style: normal;
  left: 1.5rem;
  right: 1.5rem;
}

:global(.dark .memo-document-editor),
:global(.dark .memo-document-surface) {
  background: rgb(15 23 42);
  color: rgb(226 232 240);
}

:global(.dark .memo-document-toolbar) {
  border-color: rgb(51 65 85) !important;
  background: rgb(30 41 59);
}

:global(.dark .memo-history-button) {
  color: rgb(203 213 225);
}

:global(.dark .memo-document-editor .ql-stroke) {
  stroke: rgb(203 213 225);
}

:global(.dark .memo-document-editor .ql-fill) {
  fill: rgb(203 213 225);
}

:global(.dark .memo-document-editor .ql-picker),
:global(.dark .memo-document-editor .ql-picker-options) {
  color: rgb(203 213 225);
}

:global(.dark .memo-document-editor .ql-picker-options) {
  border-color: rgb(51 65 85);
  background: rgb(30 41 59);
}

:global(.dark .memo-document-editor .ql-editor.ql-blank::before) {
  color: rgb(100 116 139);
}
</style>
