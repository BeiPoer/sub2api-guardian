<template>
  <div class="memo-sheet-editor">
    <div class="memo-sheet-toolbar">
      <div class="flex flex-wrap items-center gap-1">
        <button type="button" class="btn btn-ghost btn-sm" :disabled="disabled" @click="addRow">
          <Icon name="plus" size="xs" />
          行
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="disabled" @click="addColumn">
          <Icon name="plus" size="xs" />
          列
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="disabled" @click="removeRow">
          <Icon name="trash" size="xs" />
          当前行
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="disabled" @click="removeColumn">
          <Icon name="trash" size="xs" />
          当前列
        </button>
      </div>
      <span class="text-xs text-gray-500 dark:text-dark-400">{{ rowCount }} 行 × {{ columnCount }} 列</span>
    </div>

    <div
      ref="gridElement"
      class="memo-sheet-grid"
      tabindex="0"
      role="grid"
      :aria-rowcount="rowCount"
      :aria-colcount="columnCount"
      @keydown="handleGridKeydown"
      @copy="copySelection"
      @paste="pasteSelection"
    >
      <table>
        <thead>
          <tr>
            <th class="memo-sheet-corner" aria-hidden="true" />
            <th v-for="column in columnCount" :key="column" class="memo-sheet-column-header">
              {{ columnLabel(column - 1) }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, rowIndex) in cells" :key="rowIndex">
            <th class="memo-sheet-row-header">{{ rowIndex + 1 }}</th>
            <td
              v-for="(cell, columnIndex) in row"
              :key="columnIndex"
              role="gridcell"
              :aria-selected="isSelected(rowIndex, columnIndex)"
              :class="{
                'memo-sheet-cell-selected': isSelected(rowIndex, columnIndex),
                'memo-sheet-cell-focus': isFocusCell(rowIndex, columnIndex),
                'memo-sheet-cell-editing': isEditing(rowIndex, columnIndex)
              }"
              @mousedown.prevent="beginSelection($event, rowIndex, columnIndex)"
              @mouseenter="extendSelection(rowIndex, columnIndex)"
              @dblclick.stop="startEditing(rowIndex, columnIndex)"
            >
              <input
                v-if="isEditing(rowIndex, columnIndex)"
                v-model="editValue"
                type="text"
                aria-label="单元格内容"
                @input="syncEditingValue"
                @keydown.stop="handleEditorKeydown"
                @blur="commitEditing"
                @mousedown.stop
                @copy.stop
                @paste.stop
              />
              <span v-else :title="cell">{{ cell }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import type { SheetMemoContent } from '@/lib/types'
import { useUIStore } from '@/stores/ui'

interface CellPoint {
  row: number
  column: number
}

const MAX_ROWS = 200
const MAX_COLUMNS = 50

const props = withDefaults(
  defineProps<{
    modelValue: SheetMemoContent
    disabled?: boolean
  }>(),
  { disabled: false }
)

const emit = defineEmits<{
  (event: 'update:modelValue', value: SheetMemoContent): void
}>()

const ui = useUIStore()
const cells = ref(cloneCells(props.modelValue.cells))
const gridElement = ref<HTMLElement | null>(null)
const anchor = ref<CellPoint>({ row: 0, column: 0 })
const focus = ref<CellPoint>({ row: 0, column: 0 })
const editing = ref<CellPoint | null>(null)
const editValue = ref('')
let editOriginal = ''
let dragging = false

const rowCount = computed(() => cells.value.length)
const columnCount = computed(() => cells.value[0]?.length ?? 0)
const selection = computed(() => ({
  top: Math.min(anchor.value.row, focus.value.row),
  bottom: Math.max(anchor.value.row, focus.value.row),
  left: Math.min(anchor.value.column, focus.value.column),
  right: Math.max(anchor.value.column, focus.value.column)
}))

onMounted(() => window.addEventListener('mouseup', stopDragging))
onBeforeUnmount(() => window.removeEventListener('mouseup', stopDragging))

watch(
  () => props.modelValue.cells,
  value => {
    if (JSON.stringify(value) === JSON.stringify(cells.value)) return
    cells.value = cloneCells(value)
    editing.value = null
    clampSelection()
  },
  { deep: true }
)

function cloneCells(value: string[][]): string[][] {
  return value.map(row => [...row])
}

function emitValue() {
  emit('update:modelValue', { cells: cloneCells(cells.value) })
}

function beginSelection(event: MouseEvent, row: number, column: number) {
  commitEditing()
  const point = { row, column }
  if (!event.shiftKey) anchor.value = point
  focus.value = point
  dragging = true
  gridElement.value?.focus()
}

function extendSelection(row: number, column: number) {
  if (dragging) focus.value = { row, column }
}

function stopDragging() {
  dragging = false
}

function isSelected(row: number, column: number): boolean {
  const range = selection.value
  return row >= range.top && row <= range.bottom && column >= range.left && column <= range.right
}

function isFocusCell(row: number, column: number): boolean {
  return focus.value.row === row && focus.value.column === column
}

function isEditing(row: number, column: number): boolean {
  return editing.value?.row === row && editing.value.column === column
}

function startEditing(row: number, column: number, replacement?: string) {
  if (props.disabled) return
  if (editing.value) commitEditing()
  anchor.value = { row, column }
  focus.value = { row, column }
  editOriginal = cells.value[row][column]
  editValue.value = replacement ?? editOriginal
  editing.value = { row, column }
  if (replacement !== undefined && replacement !== editOriginal) {
    cells.value[row][column] = replacement
    emitValue()
  }
  void nextTick(() => {
    const input = gridElement.value?.querySelector<HTMLInputElement>('.memo-sheet-cell-editing input')
    input?.focus()
    if (replacement === undefined) input?.select()
  })
}

function commitEditing() {
  if (!editing.value) return
  const { row, column } = editing.value
  const changed = cells.value[row][column] !== editValue.value
  cells.value[row][column] = editValue.value
  editing.value = null
  if (changed) emitValue()
}

function cancelEditing() {
  if (!editing.value) return
  const { row, column } = editing.value
  const changed = cells.value[row][column] !== editOriginal
  cells.value[row][column] = editOriginal
  editing.value = null
  if (changed) emitValue()
  void nextTick(() => gridElement.value?.focus())
}

function syncEditingValue() {
  if (!editing.value) return
  const { row, column } = editing.value
  if (cells.value[row][column] === editValue.value) return
  cells.value[row][column] = editValue.value
  emitValue()
}

function handleEditorKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    cancelEditing()
    return
  }
  if (event.key !== 'Enter' && event.key !== 'Tab') return
  event.preventDefault()
  const point = editing.value
  commitEditing()
  if (!point) return
  if (event.key === 'Enter') {
    moveFocus(event.shiftKey ? -1 : 1, 0)
  } else {
    moveTab(event.shiftKey ? -1 : 1)
  }
  void nextTick(() => gridElement.value?.focus())
}

function handleGridKeydown(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
    event.preventDefault()
    anchor.value = { row: 0, column: 0 }
    focus.value = { row: rowCount.value - 1, column: columnCount.value - 1 }
    return
  }

  const movement: Record<string, CellPoint> = {
    ArrowUp: { row: -1, column: 0 },
    ArrowDown: { row: 1, column: 0 },
    ArrowLeft: { row: 0, column: -1 },
    ArrowRight: { row: 0, column: 1 }
  }
  const delta = movement[event.key]
  if (delta) {
    event.preventDefault()
    moveFocus(delta.row, delta.column, event.shiftKey)
    return
  }
  if (event.key === 'Tab') {
    event.preventDefault()
    moveTab(event.shiftKey ? -1 : 1)
    return
  }
  if (event.key === 'Enter' || event.key === 'F2') {
    event.preventDefault()
    startEditing(focus.value.row, focus.value.column)
    return
  }
  if ((event.key === 'Delete' || event.key === 'Backspace') && !props.disabled) {
    event.preventDefault()
    clearSelection()
    return
  }
  if (
    !props.disabled &&
    event.key.length === 1 &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.altKey
  ) {
    event.preventDefault()
    startEditing(focus.value.row, focus.value.column, event.key)
  }
}

function moveFocus(rowDelta: number, columnDelta: number, extend = false) {
  const next = {
    row: clamp(focus.value.row + rowDelta, 0, rowCount.value - 1),
    column: clamp(focus.value.column + columnDelta, 0, columnCount.value - 1)
  }
  focus.value = next
  if (!extend) anchor.value = next
  scrollFocusIntoView()
}

function moveTab(direction: number) {
  let row = focus.value.row
  let column = focus.value.column + direction
  if (column >= columnCount.value) {
    column = 0
    row = Math.min(row + 1, rowCount.value - 1)
  } else if (column < 0) {
    column = columnCount.value - 1
    row = Math.max(row - 1, 0)
  }
  anchor.value = { row, column }
  focus.value = { row, column }
  scrollFocusIntoView()
}

function scrollFocusIntoView() {
  void nextTick(() => {
    gridElement.value
      ?.querySelector<HTMLElement>('.memo-sheet-cell-focus')
      ?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  })
}

function clearSelection() {
  const range = selection.value
  let changed = false
  for (let row = range.top; row <= range.bottom; row += 1) {
    for (let column = range.left; column <= range.right; column += 1) {
      if (cells.value[row][column] !== '') changed = true
      cells.value[row][column] = ''
    }
  }
  if (changed) emitValue()
}

function copySelection(event: ClipboardEvent) {
  if (!event.clipboardData || editing.value) return
  const range = selection.value
  const value = cells.value
    .slice(range.top, range.bottom + 1)
    .map(row => row.slice(range.left, range.right + 1).join('\t'))
    .join('\n')
  event.preventDefault()
  event.clipboardData.setData('text/plain', value)
}

function pasteSelection(event: ClipboardEvent) {
  if (props.disabled || editing.value || !event.clipboardData) return
  const value = event.clipboardData.getData('text/plain')
  if (value === '') return
  event.preventDefault()

  const lines = value.replace(/\r\n?/g, '\n').split('\n')
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop()
  const incoming = lines.map(line => line.split('\t'))
  const incomingColumns = Math.max(...incoming.map(row => row.length))
  const start = { row: selection.value.top, column: selection.value.left }
  const requiredRows = start.row + incoming.length
  const requiredColumns = start.column + incomingColumns
  if (requiredRows > MAX_ROWS || requiredColumns > MAX_COLUMNS) {
    ui.notify('error', `粘贴内容超过 ${MAX_ROWS} 行 × ${MAX_COLUMNS} 列上限`)
    return
  }

  while (rowCount.value < requiredRows) cells.value.push(Array(columnCount.value).fill(''))
  if (columnCount.value < requiredColumns) {
    const extra = requiredColumns - columnCount.value
    cells.value.forEach(row => row.push(...Array(extra).fill('')))
  }
  incoming.forEach((row, rowOffset) => {
    row.forEach((cell, columnOffset) => {
      cells.value[start.row + rowOffset][start.column + columnOffset] = cell
    })
  })
  anchor.value = start
  focus.value = {
    row: requiredRows - 1,
    column: requiredColumns - 1
  }
  emitValue()
}

function addRow() {
  if (props.disabled) return
  commitEditing()
  if (rowCount.value >= MAX_ROWS) {
    ui.notify('warning', `表格最多 ${MAX_ROWS} 行`)
    return
  }
  const index = focus.value.row + 1
  cells.value.splice(index, 0, Array(columnCount.value).fill(''))
  anchor.value = { row: index, column: focus.value.column }
  focus.value = { ...anchor.value }
  emitValue()
}

function addColumn() {
  if (props.disabled) return
  commitEditing()
  if (columnCount.value >= MAX_COLUMNS) {
    ui.notify('warning', `表格最多 ${MAX_COLUMNS} 列`)
    return
  }
  const index = focus.value.column + 1
  cells.value.forEach(row => row.splice(index, 0, ''))
  anchor.value = { row: focus.value.row, column: index }
  focus.value = { ...anchor.value }
  emitValue()
}

function removeRow() {
  if (props.disabled) return
  commitEditing()
  if (rowCount.value === 1) {
    ui.notify('warning', '表格至少保留一行')
    return
  }
  const index = focus.value.row
  if (cells.value[index].some(Boolean) && !window.confirm(`第 ${index + 1} 行包含内容，确定删除吗？`)) return
  cells.value.splice(index, 1)
  clampSelection()
  emitValue()
}

function removeColumn() {
  if (props.disabled) return
  commitEditing()
  if (columnCount.value === 1) {
    ui.notify('warning', '表格至少保留一列')
    return
  }
  const index = focus.value.column
  if (cells.value.some(row => row[index] !== '') && !window.confirm(`${columnLabel(index)} 列包含内容，确定删除吗？`)) return
  cells.value.forEach(row => row.splice(index, 1))
  clampSelection()
  emitValue()
}

function clampSelection() {
  const point = {
    row: clamp(focus.value.row, 0, rowCount.value - 1),
    column: clamp(focus.value.column, 0, columnCount.value - 1)
  }
  anchor.value = point
  focus.value = point
}

function columnLabel(index: number): string {
  let label = ''
  for (let value = index + 1; value > 0; value = Math.floor((value - 1) / 26)) {
    label = String.fromCharCode(65 + ((value - 1) % 26)) + label
  }
  return label
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.max(minimum, Math.min(value, maximum))
}
</script>

<style scoped>
.memo-sheet-editor {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  background: white;
}

.memo-sheet-toolbar {
  display: flex;
  min-height: 3rem;
  flex-shrink: 0;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  border-bottom: 1px solid rgb(229 231 235);
  background: rgb(249 250 251);
  padding: 0.375rem 0.75rem;
}

.memo-sheet-grid {
  min-height: 24rem;
  flex: 1;
  overflow: auto;
  outline: none;
  background: white;
}

table {
  width: max-content;
  min-width: 100%;
  border-collapse: separate;
  border-spacing: 0;
  table-layout: fixed;
}

th,
td {
  height: 2.25rem;
  border-right: 1px solid rgb(229 231 235);
  border-bottom: 1px solid rgb(229 231 235);
}

.memo-sheet-column-header {
  position: sticky;
  top: 0;
  z-index: 3;
  width: 8.75rem;
  min-width: 8.75rem;
  background: rgb(243 244 246);
  color: rgb(75 85 99);
  font-size: 0.75rem;
  font-weight: 600;
  text-align: center;
}

.memo-sheet-row-header,
.memo-sheet-corner {
  position: sticky;
  left: 0;
  z-index: 2;
  width: 3rem;
  min-width: 3rem;
  background: rgb(243 244 246);
  color: rgb(107 114 128);
  font-size: 0.75rem;
  font-weight: 500;
  text-align: center;
}

.memo-sheet-corner {
  top: 0;
  z-index: 4;
}

td {
  position: relative;
  width: 8.75rem;
  min-width: 8.75rem;
  max-width: 8.75rem;
  cursor: cell;
  background: white;
  color: rgb(31 41 55);
  font-size: 0.8125rem;
}

td span {
  display: block;
  overflow: hidden;
  padding: 0 0.5rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.memo-sheet-cell-selected {
  background: rgb(240 253 250);
}

.memo-sheet-cell-focus::after {
  position: absolute;
  z-index: 1;
  inset: -1px;
  border: 2px solid rgb(13 148 136);
  content: '';
  pointer-events: none;
}

.memo-sheet-cell-editing {
  z-index: 2;
  padding: 0;
}

td input {
  position: absolute;
  z-index: 3;
  inset: -1px;
  width: calc(100% + 2px);
  height: calc(100% + 2px);
  border: 2px solid rgb(13 148 136);
  border-radius: 0;
  background: white;
  padding: 0 0.5rem;
  color: rgb(17 24 39);
  outline: none;
}

:global(.dark .memo-sheet-editor),
:global(.dark .memo-sheet-grid) {
  background: rgb(15 23 42);
}

:global(.dark .memo-sheet-toolbar) {
  border-color: rgb(51 65 85);
  background: rgb(30 41 59);
}

:global(.dark .memo-sheet-editor th),
:global(.dark .memo-sheet-editor td) {
  border-color: rgb(51 65 85);
}

:global(.dark .memo-sheet-column-header),
:global(.dark .memo-sheet-row-header),
:global(.dark .memo-sheet-corner) {
  background: rgb(30 41 59);
  color: rgb(148 163 184);
}

:global(.dark .memo-sheet-editor td) {
  background: rgb(15 23 42);
  color: rgb(226 232 240);
}

:global(.dark .memo-sheet-cell-selected) {
  background: rgb(19 78 74 / 0.35);
}

:global(.dark .memo-sheet-editor td input) {
  background: rgb(15 23 42);
  color: rgb(226 232 240);
}
</style>
