import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ToastKind = 'success' | 'error' | 'warning' | 'info'

export interface Toast {
  id: number
  kind: ToastKind
  message: string
}

/** UI 状态：侧栏折叠、深浅色、轻提示。 */
export const useUIStore = defineStore('ui', () => {
  const sidebarCollapsed = ref(localStorage.getItem('guardian.sidebar') === 'collapsed')
  const dark = ref(resolveInitialTheme())
  const toasts = ref<Toast[]>([])
  let seq = 0

  applyTheme(dark.value)

  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value
    localStorage.setItem('guardian.sidebar', sidebarCollapsed.value ? 'collapsed' : 'expanded')
  }

  function toggleTheme() {
    dark.value = !dark.value
    localStorage.setItem('guardian.theme', dark.value ? 'dark' : 'light')
    applyTheme(dark.value)
  }

  function notify(kind: ToastKind, message: string) {
    const id = ++seq
    toasts.value.push({ id, kind, message })
    window.setTimeout(() => dismiss(id), 4000)
  }

  function dismiss(id: number) {
    toasts.value = toasts.value.filter(item => item.id !== id)
  }

  return { sidebarCollapsed, dark, toasts, toggleSidebar, toggleTheme, notify, dismiss }
})

function resolveInitialTheme(): boolean {
  const saved = localStorage.getItem('guardian.theme')
  if (saved) return saved === 'dark'
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function applyTheme(dark: boolean) {
  document.documentElement.classList.toggle('dark', dark)
}
