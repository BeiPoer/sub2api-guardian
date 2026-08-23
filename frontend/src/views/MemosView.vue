<template>
  <AppLayout title="备忘录" subtitle="记录文档和轻量表格">
    <section class="memo-workspace">
      <aside class="memo-list-panel" :class="mobileEditorOpen && 'hidden lg:flex'">
        <div class="memo-list-header">
          <div>
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">全部备忘录</h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ memos.length }} 项</p>
          </div>
          <button type="button" class="btn btn-primary btn-sm" :disabled="loadingList" @click="requestCreate">
            <Icon name="plus" size="sm" />
            新增
          </button>
        </div>

        <label class="memo-search">
          <Icon name="search" size="sm" class="text-gray-400" />
          <input v-model="search" type="search" placeholder="搜索标题" aria-label="搜索备忘录" />
        </label>

        <div v-if="loadingList" class="flex flex-1 items-center justify-center">
          <span class="spinner text-primary-500" />
        </div>
        <div v-else-if="listError" class="flex flex-1 flex-col items-center justify-center gap-3 px-5 text-center">
          <Icon name="exclamationCircle" size="lg" class="text-red-500" />
          <p class="text-sm text-gray-600 dark:text-dark-300">{{ listError }}</p>
          <button type="button" class="btn btn-secondary btn-sm" @click="loadList">重新加载</button>
        </div>
        <div v-else-if="filteredMemos.length" class="memo-list-scroll">
          <div
            v-for="memo in filteredMemos"
            :key="memo.id"
            class="memo-list-item"
            :class="selectedMemo?.id === memo.id && 'memo-list-item-active'"
          >
            <button type="button" class="memo-list-main" @click="selectMemo(memo)">
              <span class="memo-list-icon" :class="memo.type === 'sheet' && 'memo-list-icon-sheet'">
                <Icon :name="memo.type === 'document' ? 'document' : 'grid'" size="sm" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ memo.title }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-dark-400">
                  {{ memo.type === 'document' ? '文档' : '表格' }} · {{ formatRelative(memo.updated_at) }}
                </span>
              </span>
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-icon memo-list-delete"
              :disabled="deletingID === memo.id"
              :aria-label="`删除${memo.title}`"
              title="删除"
              @click="requestDelete(memo)"
            >
              <Icon name="trash" size="xs" />
            </button>
          </div>
        </div>
        <div v-else class="flex flex-1 flex-col items-center justify-center px-5 text-center">
          <Icon :name="search ? 'search' : 'clipboard'" size="xl" class="text-gray-300 dark:text-dark-600" />
          <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ search ? '没有匹配的备忘录' : '暂无备忘录' }}
          </p>
          <button v-if="!search" type="button" class="btn btn-primary btn-sm mt-4" @click="requestCreate">
            <Icon name="plus" size="sm" />
            新增备忘录
          </button>
        </div>
      </aside>

      <div class="memo-editor-panel" :class="mobileEditorOpen ? 'flex' : 'hidden lg:flex'">
        <div v-if="loadingDetail" class="flex flex-1 items-center justify-center">
          <span class="spinner text-primary-500" />
        </div>
        <div v-else-if="detailError" class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
          <Icon name="exclamationCircle" size="xl" class="text-red-500" />
          <p class="text-sm text-gray-600 dark:text-dark-300">{{ detailError }}</p>
          <button
            v-if="selectedID"
            type="button"
            class="btn btn-secondary btn-sm"
            @click="loadMemo(detailTargetID, true)"
          >
            重新加载
          </button>
        </div>
        <template v-else-if="selectedMemo">
          <header class="memo-editor-header">
            <button
              type="button"
              class="btn btn-ghost btn-icon flex-shrink-0 lg:hidden"
              aria-label="返回备忘录列表"
              title="返回列表"
              @click="backToList"
            >
              <Icon name="arrowLeft" size="sm" />
            </button>
            <div class="min-w-0 flex-1">
              <input
                v-model="draftTitle"
                class="memo-title-input"
                aria-label="备忘录标题"
                :disabled="saving"
              />
              <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
                <span>{{ selectedMemo.type === 'document' ? '文档' : '表格' }}</span>
                <span aria-hidden="true">·</span>
                <span>{{ formatRelative(selectedMemo.updated_at) }}</span>
                <span aria-hidden="true">·</span>
                <span :class="dirty ? 'font-medium text-amber-600 dark:text-amber-400' : 'text-emerald-600 dark:text-emerald-400'">
                  {{ dirty ? '未保存' : '已保存' }}
                </span>
              </div>
            </div>
            <div class="flex flex-shrink-0 items-center gap-1.5">
              <button
                type="button"
                class="btn btn-ghost btn-icon"
                :disabled="saving || restoringArchive"
                aria-label="最近恢复点"
                title="最近恢复点"
                @click="openHistory"
              >
                <Icon name="clock" size="sm" />
              </button>
              <button
                type="button"
                class="btn btn-primary btn-sm"
                :disabled="!dirty || saving"
                @click="saveMemo"
              >
                <Icon name="check" size="sm" />
                <span class="hidden sm:inline">{{ saving ? '保存中…' : '保存' }}</span>
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-icon text-red-500 hover:text-red-600"
                :disabled="deletingID === selectedMemo.id"
                aria-label="删除备忘录"
                title="删除"
                @click="requestDelete(selectedMemo)"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </header>

          <MemoDocumentEditor
            v-if="selectedMemo.type === 'document'"
            :key="`document-${selectedMemo.id}`"
            v-model="documentContent"
            :disabled="saving"
          />
          <MemoSheetEditor
            v-else
            :key="`sheet-${selectedMemo.id}`"
            v-model="sheetContent"
            :disabled="saving"
          />
        </template>
        <div v-else class="flex flex-1 flex-col items-center justify-center px-6 text-center">
          <Icon name="clipboard" size="xl" class="text-gray-300 dark:text-dark-600" />
          <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">选择或新增一条备忘录</p>
        </div>
      </div>
    </section>

    <Modal :open="createModalOpen" title="新增备忘录" @close="closeCreateModal">
      <form id="memo-create-form" class="space-y-5" @submit.prevent="createMemo">
        <label class="block">
          <span class="input-label">标题</span>
          <input v-model="createForm.title" class="input" autofocus placeholder="输入备忘录标题" />
        </label>
        <div>
          <span class="input-label">类型</span>
          <SegmentedControl v-model="createForm.type" :options="memoTypeOptions" />
        </div>
      </form>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="creating" @click="closeCreateModal">取消</button>
        <button type="submit" form="memo-create-form" class="btn btn-primary" :disabled="creating">
          <Icon name="plus" size="sm" />
          {{ creating ? '创建中…' : '创建' }}
        </button>
      </template>
    </Modal>

    <Modal :open="historyModalOpen" title="最近恢复点" @close="closeHistory">
      <div v-if="loadingArchives" class="flex min-h-64 items-center justify-center">
        <span class="spinner text-primary-500" />
      </div>
      <div v-else-if="historyError" class="flex min-h-64 flex-col items-center justify-center gap-3 text-center">
        <Icon name="exclamationCircle" size="lg" class="text-red-500" />
        <p class="text-sm text-gray-600 dark:text-dark-300">{{ historyError }}</p>
        <button type="button" class="btn btn-secondary btn-sm" @click="loadArchives">重新加载</button>
      </div>
      <div v-else-if="archives.length" class="memo-archive-layout">
        <div class="memo-archive-list" role="list" aria-label="恢复点列表">
          <button
            v-for="archive in archives"
            :key="archive.id"
            type="button"
            class="memo-archive-item"
            :class="selectedArchiveID === archive.id && 'memo-archive-item-active'"
            :aria-pressed="selectedArchiveID === archive.id"
            @click="selectedArchiveID = archive.id"
          >
            <span class="block text-sm font-medium text-gray-900 dark:text-white">
              {{ formatTime(archive.created_at) }}
            </span>
            <span class="mt-1 block truncate text-xs text-gray-500 dark:text-dark-400">
              版本 {{ archive.source_revision }} · {{ archive.title }}
            </span>
          </button>
        </div>

        <section v-if="selectedArchive" class="memo-archive-preview" aria-label="恢复点预览">
          <header class="memo-archive-preview-header">
            <strong class="truncate text-sm text-gray-900 dark:text-white">{{ selectedArchive.title }}</strong>
            <span class="text-xs text-gray-500 dark:text-dark-400">版本 {{ selectedArchive.source_revision }}</span>
          </header>
          <pre v-if="selectedMemo?.type === 'document'" class="memo-archive-document">{{ archiveDocumentText || '空白文档' }}</pre>
          <div
            v-else-if="archiveSheetContent"
            class="memo-archive-sheet"
            :class="archiveSheetContent.wrap_text !== false && 'memo-archive-sheet-wrapping'"
          >
            <table :style="{ width: `${archiveSheetTableWidth}px` }">
              <colgroup>
                <col class="memo-archive-row-number-column" />
                <col
                  v-for="column in archiveSheetColumnCount"
                  :key="column"
                  :style="{ width: `${archiveSheetColumnWidths[column - 1]}px` }"
                />
              </colgroup>
              <thead>
                <tr>
                  <th aria-hidden="true" />
                  <th v-for="column in archiveSheetColumnCount" :key="column">
                    {{ archiveColumnLabel(column - 1) }}
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, rowIndex) in archiveSheetContent.cells" :key="rowIndex">
                  <th>{{ rowIndex + 1 }}</th>
                  <td v-for="(cell, columnIndex) in row" :key="columnIndex" :title="cell">{{ cell }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
      <div v-else class="flex min-h-64 flex-col items-center justify-center text-center">
        <Icon name="clock" size="xl" class="text-gray-300 dark:text-dark-600" />
        <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">暂无恢复点</p>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="restoringArchive" @click="closeHistory">关闭</button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!selectedArchive || loadingArchives || restoringArchive"
          @click="requestRestoreArchive"
        >
          <Icon name="refresh" size="sm" />
          {{ restoringArchive ? '恢复中…' : '恢复此版本' }}
        </button>
      </template>
    </Modal>

    <Modal :open="dirtyModalOpen" title="有未保存的修改" @close="cancelPendingAction">
      <p class="text-sm leading-6 text-gray-600 dark:text-dark-300">
        当前备忘录还有未保存内容。保存后继续、放弃这些修改，或留在当前页面。
      </p>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="cancelPendingAction">取消</button>
        <button type="button" class="btn btn-ghost text-red-600" :disabled="saving" @click="discardAndContinue">
          放弃并继续
        </button>
        <button type="button" class="btn btn-primary" :disabled="saving" @click="saveAndContinue">
          <Icon name="check" size="sm" />
          {{ saving ? '保存中…' : '保存并继续' }}
        </button>
      </template>
    </Modal>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import Icon from '@/components/Icon.vue'
import MemoDocumentEditor from '@/components/MemoDocumentEditor.vue'
import MemoSheetEditor from '@/components/MemoSheetEditor.vue'
import Modal from '@/components/Modal.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import { ApiError, api } from '@/lib/api'
import { formatRelative, formatTime } from '@/lib/format'
import type {
  DocumentMemoContent,
  Memo,
  MemoArchive,
  MemoSummary,
  MemoType,
  SheetMemoContent
} from '@/lib/types'
import { useUIStore } from '@/stores/ui'

type PendingAction = () => unknown | Promise<unknown>

const route = useRoute()
const router = useRouter()
const ui = useUIStore()
const memos = ref<MemoSummary[]>([])
const selectedMemo = ref<Memo | null>(null)
const draftTitle = ref('')
const draftContent = ref<Memo['content']>({ ops: [{ insert: '\n' }] })
const baselineSignature = ref('')
const search = ref('')
const loadingList = ref(true)
const loadingDetail = ref(false)
const saving = ref(false)
const creating = ref(false)
const deletingID = ref<number | null>(null)
const listError = ref('')
const detailError = ref('')
const createModalOpen = ref(false)
const dirtyModalOpen = ref(false)
const historyModalOpen = ref(false)
const mobileEditorOpen = ref(false)
const loadingArchives = ref(false)
const restoringArchive = ref(false)
const archives = ref<MemoArchive[]>([])
const selectedArchiveID = ref<number | null>(null)
const historyError = ref('')
const detailTargetID = ref(0)
let pendingAction: PendingAction | null = null
let detailRequest = 0
let archiveRequest = 0
let listLoaded = false

const createForm = reactive<{ title: string; type: MemoType }>({
  title: '',
  type: 'document'
})

const memoTypeOptions = [
  { value: 'document', label: '文档', icon: 'document' as const },
  { value: 'sheet', label: '表格', icon: 'grid' as const }
]

const selectedID = computed(() => selectedMemo.value?.id ?? null)
const filteredMemos = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return needle ? memos.value.filter(memo => memo.title.toLowerCase().includes(needle)) : memos.value
})
const dirty = computed(() => selectedMemo.value !== null && draftSignature() !== baselineSignature.value)
const documentContent = computed<DocumentMemoContent>({
  get: () => draftContent.value as DocumentMemoContent,
  set: value => {
    draftContent.value = value
  }
})
const sheetContent = computed<SheetMemoContent>({
  get: () => draftContent.value as SheetMemoContent,
  set: value => {
    draftContent.value = value
  }
})
const selectedArchive = computed(() =>
  archives.value.find(archive => archive.id === selectedArchiveID.value) ?? null
)
const archiveDocumentText = computed(() => {
  if (!selectedArchive.value || selectedMemo.value?.type !== 'document') return ''
  return (selectedArchive.value.content as DocumentMemoContent).ops.map(operation => operation.insert).join('')
})
const archiveSheetContent = computed(() => {
  if (!selectedArchive.value || selectedMemo.value?.type !== 'sheet') return null
  return selectedArchive.value.content as SheetMemoContent
})
const archiveSheetColumnCount = computed(() => archiveSheetContent.value?.cells[0]?.length ?? 0)
const archiveSheetColumnWidths = computed(() =>
  Array.from({ length: archiveSheetColumnCount.value }, (_, column) => {
    const width = archiveSheetContent.value?.column_widths?.[column]
    return typeof width === 'number' ? Math.max(72, Math.min(600, width)) : 140
  })
)
const archiveSheetTableWidth = computed(() =>
  40 + archiveSheetColumnWidths.value.reduce((total, width) => total + width, 0)
)

onMounted(() => {
  window.addEventListener('beforeunload', handleBeforeUnload)
  void loadList()
})

onBeforeUnmount(() => window.removeEventListener('beforeunload', handleBeforeUnload))

watch(
  () => route.query.id,
  raw => {
    if (!listLoaded) return
    const target = findMemo(parseMemoID(raw))
    if (!target) {
      const fallback = memos.value[0]
      if (fallback) void replaceRouteID(fallback.id)
      return
    }
    if (selectedMemo.value?.id === target.id) return
    void loadMemo(target.id, true)
  }
)

onBeforeRouteUpdate((to, from) => {
  if (!dirty.value || to.query.id === from.query.id) return true
  queuePendingAction(() => router.push(to.fullPath))
  return false
})

onBeforeRouteLeave(to => {
  if (!dirty.value) return true
  queuePendingAction(() => router.push(to.fullPath))
  return false
})

async function loadList() {
  loadingList.value = true
  listError.value = ''
  try {
    const data = await api.memos()
    memos.value = sortMemos(data.items ?? [])
    listLoaded = true
    if (!memos.value.length) {
      clearSelection()
      await router.replace({ path: '/tools/memos' })
      return
    }
    const requestedID = parseMemoID(route.query.id)
    const target = findMemo(requestedID) ?? memos.value[0]
    await loadMemo(target.id, requestedID === target.id)
    if (requestedID !== target.id) await replaceRouteID(target.id)
  } catch (error) {
    listError.value = (error as Error).message
  } finally {
    loadingList.value = false
  }
}

async function loadMemo(id: number, openOnMobile: boolean) {
  const request = ++detailRequest
  detailTargetID.value = id
  loadingDetail.value = true
  detailError.value = ''
  try {
    const memo = await api.memo(id)
    if (request !== detailRequest) return
    applyMemo(memo)
    if (openOnMobile) mobileEditorOpen.value = true
  } catch (error) {
    if (request !== detailRequest) return
    detailError.value = (error as Error).message
  } finally {
    if (request === detailRequest) loadingDetail.value = false
  }
}

function selectMemo(memo: MemoSummary) {
  if (selectedMemo.value?.id === memo.id) {
    mobileEditorOpen.value = true
    return
  }
  runAfterDirty(() => router.push({ path: '/tools/memos', query: { id: String(memo.id) } }))
}

function requestCreate() {
  runAfterDirty(openCreateModal)
}

function openCreateModal() {
  Object.assign(createForm, { title: '', type: 'document' as MemoType })
  createModalOpen.value = true
}

function closeCreateModal() {
  if (!creating.value) createModalOpen.value = false
}

async function createMemo() {
  const title = createForm.title.trim()
  if (!title) {
    ui.notify('error', '请填写备忘录标题')
    return
  }
  if (Array.from(title).length > 100) {
    ui.notify('error', '备忘录标题不能超过 100 个字符')
    return
  }
  creating.value = true
  try {
    const memo = await api.createMemo({ title, type: createForm.type })
    upsertSummary(memo)
    applyMemo(memo)
    createModalOpen.value = false
    mobileEditorOpen.value = true
    await router.push({ path: '/tools/memos', query: { id: String(memo.id) } })
    ui.notify('success', '备忘录已创建')
  } catch (error) {
    ui.notify('error', (error as Error).message)
  } finally {
    creating.value = false
  }
}

function openHistory() {
  if (!selectedMemo.value) return
  historyModalOpen.value = true
  void loadArchives()
}

async function loadArchives() {
  const memo = selectedMemo.value
  if (!memo) return
  const request = ++archiveRequest
  loadingArchives.value = true
  historyError.value = ''
  try {
    const response = await api.memoArchives(memo.id)
    if (request !== archiveRequest || selectedMemo.value?.id !== memo.id) return
    archives.value = response.items ?? []
    selectedArchiveID.value = archives.value[0]?.id ?? null
  } catch (error) {
    if (request !== archiveRequest) return
    historyError.value = (error as Error).message
  } finally {
    if (request === archiveRequest) loadingArchives.value = false
  }
}

function closeHistory() {
  if (restoringArchive.value) return
  archiveRequest += 1
  historyModalOpen.value = false
  archives.value = []
  selectedArchiveID.value = null
  historyError.value = ''
  loadingArchives.value = false
}

function requestRestoreArchive() {
  const archive = selectedArchive.value
  if (!archive) return
  runAfterDirty(() => confirmAndRestoreArchive(archive.id))
}

async function confirmAndRestoreArchive(archiveID: number) {
  if (!window.confirm('确定恢复此版本吗？当前服务器版本会先自动保存为恢复点。')) return
  await restoreArchive(archiveID, false)
}

async function restoreArchive(archiveID: number, force: boolean) {
  const memo = selectedMemo.value
  if (!memo || restoringArchive.value) return
  restoringArchive.value = true
  let retryWithForce = false
  try {
    const restored = await api.restoreMemoArchive(memo.id, archiveID, memo.revision, force)
    applySavedMemo(restored)
    archiveRequest += 1
    historyModalOpen.value = false
    archives.value = []
    selectedArchiveID.value = null
    ui.notify('success', '备忘录已恢复')
  } catch (error) {
    if (!force && isMemoConflict(error)) {
      retryWithForce = window.confirm('其他页面已有更新。继续恢复会覆盖服务器最新版，确定恢复吗？')
    } else {
      ui.notify('error', (error as Error).message)
    }
  } finally {
    restoringArchive.value = false
  }
  if (retryWithForce) await restoreArchive(archiveID, true)
}

async function saveMemo(): Promise<boolean> {
  const memo = selectedMemo.value
  if (!memo || saving.value) return false
  const title = draftTitle.value.trim()
  if (!title) {
    ui.notify('error', '备忘录标题不能为空')
    return false
  }
  if (Array.from(title).length > 100) {
    ui.notify('error', '备忘录标题不能超过 100 个字符')
    return false
  }

  saving.value = true
  let retryWithForce = false
  try {
    const saved = await api.updateMemo(memo.id, {
      title,
      content: cloneContent(draftContent.value),
      expected_revision: memo.revision
    })
    applySavedMemo(saved)
    ui.notify('success', '备忘录已保存')
    return true
  } catch (error) {
    if (isMemoConflict(error)) {
      retryWithForce = window.confirm('其他页面已有更新。继续覆盖会丢失对方的最新修改，确定覆盖吗？')
    } else {
      ui.notify('error', (error as Error).message)
    }
  } finally {
    saving.value = false
  }

  if (!retryWithForce) return false
  return forceSaveMemo()
}

async function forceSaveMemo(): Promise<boolean> {
  const memo = selectedMemo.value
  if (!memo) return false
  saving.value = true
  try {
    const saved = await api.updateMemo(memo.id, {
      title: draftTitle.value.trim(),
      content: cloneContent(draftContent.value),
      expected_revision: memo.revision,
      force: true
    })
    applySavedMemo(saved)
    ui.notify('success', '备忘录已覆盖保存')
    return true
  } catch (error) {
    ui.notify('error', (error as Error).message)
    return false
  } finally {
    saving.value = false
  }
}

function requestDelete(memo: MemoSummary) {
  const action = () => confirmAndDelete(memo)
  if (selectedMemo.value?.id === memo.id) runAfterDirty(action)
  else void action()
}

async function confirmAndDelete(memo: MemoSummary) {
  if (!window.confirm(`确定永久删除“${memo.title}”吗？`)) return
  const revision = selectedMemo.value?.id === memo.id ? selectedMemo.value.revision : memo.revision
  await deleteMemo(memo.id, revision, false)
}

async function deleteMemo(id: number, revision: number, force: boolean) {
  deletingID.value = id
  let retryWithForce = false
  try {
    await api.deleteMemo(id, revision, force)
    const wasSelected = selectedMemo.value?.id === id
    memos.value = memos.value.filter(memo => memo.id !== id)
    if (wasSelected) await selectAfterDelete()
    ui.notify('success', '备忘录已删除')
  } catch (error) {
    if (!force && isMemoConflict(error)) {
      retryWithForce = window.confirm('该备忘录已在其他页面修改。确定仍删除服务器上的最新版本吗？')
    } else {
      ui.notify('error', (error as Error).message)
    }
  } finally {
    deletingID.value = null
  }
  if (retryWithForce) await deleteMemo(id, revision, true)
}

async function selectAfterDelete() {
  clearSelection()
  mobileEditorOpen.value = false
  const next = memos.value[0]
  if (!next) {
    await router.replace({ path: '/tools/memos' })
    return
  }
  await loadMemo(next.id, false)
  await replaceRouteID(next.id)
}

function backToList() {
  runAfterDirty(() => {
    mobileEditorOpen.value = false
  })
}

function runAfterDirty(action: PendingAction) {
  if (dirty.value) {
    queuePendingAction(action)
    return
  }
  void action()
}

function queuePendingAction(action: PendingAction) {
  pendingAction = action
  dirtyModalOpen.value = true
}

function cancelPendingAction() {
  if (saving.value) return
  pendingAction = null
  dirtyModalOpen.value = false
}

async function saveAndContinue() {
  if (!(await saveMemo())) return
  await continuePendingAction()
}

async function discardAndContinue() {
  discardDraft()
  await continuePendingAction()
}

async function continuePendingAction() {
  const action = pendingAction
  pendingAction = null
  dirtyModalOpen.value = false
  if (action) await action()
}

function discardDraft() {
  if (!selectedMemo.value) return
  draftTitle.value = selectedMemo.value.title
  draftContent.value = cloneContent(selectedMemo.value.content)
  baselineSignature.value = draftSignature()
}

function applyMemo(memo: Memo) {
  if (selectedMemo.value?.id !== memo.id) {
    archiveRequest += 1
    historyModalOpen.value = false
    archives.value = []
    selectedArchiveID.value = null
    historyError.value = ''
    loadingArchives.value = false
  }
  selectedMemo.value = { ...memo, content: cloneContent(memo.content) }
  draftTitle.value = memo.title
  draftContent.value = cloneContent(memo.content)
  baselineSignature.value = draftSignature()
  detailError.value = ''
}

function applySavedMemo(memo: Memo) {
  applyMemo(memo)
  upsertSummary(memo)
}

function clearSelection() {
  detailRequest += 1
  archiveRequest += 1
  selectedMemo.value = null
  draftTitle.value = ''
  draftContent.value = { ops: [{ insert: '\n' }] }
  baselineSignature.value = ''
  detailError.value = ''
  loadingDetail.value = false
  historyModalOpen.value = false
  archives.value = []
  selectedArchiveID.value = null
}

function upsertSummary(memo: MemoSummary) {
  const summary: MemoSummary = {
    id: memo.id,
    title: memo.title,
    type: memo.type,
    revision: memo.revision,
    created_at: memo.created_at,
    updated_at: memo.updated_at
  }
  memos.value = sortMemos([...memos.value.filter(item => item.id !== summary.id), summary])
}

function sortMemos(items: MemoSummary[]): MemoSummary[] {
  return [...items].sort((left, right) =>
    right.updated_at.localeCompare(left.updated_at) || right.id - left.id
  )
}

function findMemo(id: number | null): MemoSummary | undefined {
  return id === null ? undefined : memos.value.find(memo => memo.id === id)
}

function parseMemoID(value: unknown): number | null {
  const raw = Array.isArray(value) ? value[0] : value
  if (typeof raw !== 'string' || !/^\d+$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

function archiveColumnLabel(index: number): string {
  let label = ''
  for (let value = index + 1; value > 0; value = Math.floor((value - 1) / 26)) {
    label = String.fromCharCode(65 + ((value - 1) % 26)) + label
  }
  return label
}

function replaceRouteID(id: number) {
  return router.replace({ path: '/tools/memos', query: { id: String(id) } })
}

function draftSignature(): string {
  return JSON.stringify({
    title: draftTitle.value,
    content: draftContent.value,
    revision: selectedMemo.value?.revision ?? 0
  })
}

function cloneContent<T extends Memo['content']>(content: T): T {
  return JSON.parse(JSON.stringify(content)) as T
}

function isMemoConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.code === 'MEMO_CONFLICT'
}

function handleBeforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}
</script>

<style scoped>
.memo-workspace {
  display: grid;
  height: calc(100vh - 8.5rem);
  min-height: 34rem;
  overflow: hidden;
  border: 1px solid rgb(229 231 235);
  border-radius: 0.5rem;
  background: white;
  box-shadow: 0 1px 2px rgb(0 0 0 / 0.04);
}

.memo-list-panel {
  min-width: 0;
  flex-direction: column;
  background: rgb(249 250 251);
}

.memo-list-header {
  display: flex;
  min-height: 4.5rem;
  flex-shrink: 0;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border-bottom: 1px solid rgb(229 231 235);
  padding: 0.75rem 1rem;
}

.memo-search {
  display: flex;
  height: 2.5rem;
  flex-shrink: 0;
  align-items: center;
  gap: 0.5rem;
  margin: 0.75rem;
  border: 1px solid rgb(209 213 219);
  border-radius: 0.5rem;
  background: white;
  padding: 0 0.75rem;
}

.memo-search:focus-within {
  border-color: rgb(20 184 166);
  box-shadow: 0 0 0 3px rgb(20 184 166 / 0.15);
}

.memo-search input {
  min-width: 0;
  flex: 1;
  border: 0;
  background: transparent;
  color: rgb(31 41 55);
  font-size: 0.875rem;
  outline: none;
}

.memo-list-scroll {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0 0.5rem 0.75rem;
}

.memo-list-item {
  display: flex;
  min-height: 4.25rem;
  align-items: center;
  border: 1px solid transparent;
  border-radius: 0.5rem;
  color: rgb(55 65 81);
}

.memo-list-item:hover {
  background: rgb(243 244 246);
}

.memo-list-item-active {
  border-color: rgb(153 246 228);
  background: rgb(240 253 250);
}

.memo-list-main {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 0.75rem;
  padding: 0.625rem 0.25rem 0.625rem 0.625rem;
  text-align: left;
}

.memo-list-icon {
  display: flex;
  width: 2rem;
  height: 2rem;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  border-radius: 0.5rem;
  background: rgb(204 251 241);
  color: rgb(13 148 136);
}

.memo-list-icon-sheet {
  background: rgb(219 234 254);
  color: rgb(37 99 235);
}

.memo-list-delete {
  margin-right: 0.25rem;
  padding: 0.5rem;
  color: rgb(156 163 175);
}

.memo-list-delete:hover {
  color: rgb(220 38 38);
}

.memo-editor-panel {
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  background: white;
}

.memo-editor-header {
  display: flex;
  min-height: 4.5rem;
  flex-shrink: 0;
  align-items: center;
  gap: 0.75rem;
  border-bottom: 1px solid rgb(229 231 235);
  padding: 0.625rem 0.75rem;
}

.memo-title-input {
  display: block;
  width: 100%;
  min-width: 0;
  border: 0;
  background: transparent;
  color: rgb(17 24 39);
  font-size: 1rem;
  font-weight: 600;
  line-height: 1.5rem;
  outline: none;
}

.memo-title-input:focus {
  box-shadow: inset 0 -1px rgb(20 184 166);
}

.memo-archive-layout {
  display: grid;
  min-height: 20rem;
  overflow: hidden;
  border: 1px solid rgb(229 231 235);
  border-radius: 0.375rem;
  background: white;
}

.memo-archive-list {
  display: flex;
  overflow-x: auto;
  border-bottom: 1px solid rgb(229 231 235);
  background: rgb(249 250 251);
}

.memo-archive-item {
  min-width: 10rem;
  border-right: 1px solid rgb(229 231 235);
  padding: 0.75rem;
  text-align: left;
}

.memo-archive-item:hover {
  background: rgb(243 244 246);
}

.memo-archive-item-active {
  background: rgb(240 253 250);
  box-shadow: inset 3px 0 rgb(13 148 136);
}

.memo-archive-preview {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
}

.memo-archive-preview-header {
  display: flex;
  min-height: 3rem;
  flex-shrink: 0;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border-bottom: 1px solid rgb(229 231 235);
  padding: 0.625rem 0.75rem;
}

.memo-archive-document {
  min-height: 17rem;
  max-height: 26rem;
  overflow: auto;
  margin: 0;
  padding: 1rem;
  color: rgb(31 41 55);
  font-family: inherit;
  font-size: 0.875rem;
  line-height: 1.65;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.memo-archive-sheet {
  min-height: 17rem;
  max-height: 26rem;
  overflow: auto;
  background: white;
}

.memo-archive-sheet table {
  border-collapse: separate;
  border-spacing: 0;
  table-layout: fixed;
}

.memo-archive-sheet th,
.memo-archive-sheet td {
  box-sizing: border-box;
  height: 2rem;
  border-right: 1px solid rgb(229 231 235);
  border-bottom: 1px solid rgb(229 231 235);
  font-size: 0.75rem;
}

.memo-archive-sheet thead th {
  position: sticky;
  z-index: 2;
  top: 0;
  background: rgb(243 244 246);
  color: rgb(75 85 99);
  text-align: center;
}

.memo-archive-sheet tbody th,
.memo-archive-sheet thead th:first-child {
  position: sticky;
  z-index: 1;
  left: 0;
  width: 2.5rem;
  background: rgb(243 244 246);
  color: rgb(107 114 128);
  font-weight: 500;
  text-align: center;
}

.memo-archive-sheet thead th:first-child {
  z-index: 3;
}

.memo-archive-row-number-column {
  width: 2.5rem;
}

.memo-archive-sheet td {
  overflow: hidden;
  padding: 0 0.375rem;
  color: rgb(31 41 55);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.memo-archive-sheet-wrapping td {
  height: auto;
  padding: 0.375rem;
  line-height: 1.125rem;
  overflow-wrap: anywhere;
  text-overflow: clip;
  vertical-align: top;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (min-width: 640px) {
  .memo-archive-layout {
    grid-template-columns: 13rem minmax(0, 1fr);
  }

  .memo-archive-list {
    flex-direction: column;
    overflow-x: hidden;
    overflow-y: auto;
    border-right: 1px solid rgb(229 231 235);
    border-bottom: 0;
  }

  .memo-archive-item {
    min-width: 0;
    border-right: 0;
    border-bottom: 1px solid rgb(229 231 235);
  }
}

@media (min-width: 1024px) {
  .memo-workspace {
    grid-template-columns: 19rem minmax(0, 1fr);
  }

  .memo-list-panel {
    display: flex !important;
    border-right: 1px solid rgb(229 231 235);
  }

  .memo-editor-panel {
    display: flex !important;
  }
}

:global(.dark .memo-workspace),
:global(.dark .memo-editor-panel) {
  border-color: rgb(51 65 85);
  background: rgb(15 23 42);
}

:global(.dark .memo-list-panel),
:global(.dark .memo-list-header) {
  border-color: rgb(51 65 85);
  background: rgb(15 23 42);
}

:global(.dark .memo-search) {
  border-color: rgb(71 85 105);
  background: rgb(30 41 59);
}

:global(.dark .memo-search input),
:global(.dark .memo-title-input) {
  color: rgb(241 245 249);
}

:global(.dark .memo-list-item:hover) {
  background: rgb(30 41 59);
}

:global(.dark .memo-list-item-active) {
  border-color: rgb(19 78 74);
  background: rgb(19 78 74 / 0.3);
}

:global(.dark .memo-editor-header) {
  border-color: rgb(51 65 85);
}

:global(.dark .memo-archive-layout),
:global(.dark .memo-archive-preview),
:global(.dark .memo-archive-sheet) {
  border-color: rgb(51 65 85);
  background: rgb(15 23 42);
}

:global(.dark .memo-archive-list),
:global(.dark .memo-archive-item),
:global(.dark .memo-archive-preview-header) {
  border-color: rgb(51 65 85);
  background: rgb(30 41 59);
}

:global(.dark .memo-archive-item:hover) {
  background: rgb(51 65 85);
}

:global(.dark .memo-archive-item-active) {
  background: rgb(19 78 74 / 0.35);
}

:global(.dark .memo-archive-document),
:global(.dark .memo-archive-sheet td) {
  color: rgb(226 232 240);
}

:global(.dark .memo-archive-sheet th),
:global(.dark .memo-archive-sheet td) {
  border-color: rgb(51 65 85);
}

:global(.dark .memo-archive-sheet thead th),
:global(.dark .memo-archive-sheet tbody th) {
  background: rgb(30 41 59);
  color: rgb(148 163 184);
}
</style>
