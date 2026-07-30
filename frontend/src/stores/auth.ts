import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, setUnauthorizedHandler, UnauthorizedError } from '@/lib/api'

/**
 * AuthState 是整个面板的入口分流依据：
 *
 * - loading：还在探测，先什么都别渲染，避免登录页一闪而过
 * - setup：一个用户都没有，走初始化向导
 * - login：需要登录
 * - ready：已登录，正常渲染面板
 */
export type AuthState = 'loading' | 'setup' | 'login' | 'ready'

export const useAuthStore = defineStore('auth', () => {
  const state = ref<AuthState>('loading')
  const username = ref('')
  const error = ref('')
  const busy = ref(false)

  const authenticated = computed(() => state.value === 'ready')

  /**
   * bootstrap 决定进哪个界面。
   *
   * 顺序不能反：先问需不需要初始化，再问登录态。空库时 /api/me 必然 401，
   * 反过来会让首次部署的用户先看到一个没有账号可用的登录页。
   */
  async function bootstrap() {
    // 任何请求遇到 401 都把界面切回登录页，会话过期时不必手动刷新。
    setUnauthorizedHandler(() => {
      if (state.value === 'ready') {
        state.value = 'login'
        username.value = ''
      }
    })

    try {
      const status = await api.setupStatus()
      if (status.needs_setup) {
        state.value = 'setup'
        return
      }
    } catch (err) {
      error.value = (err as Error).message
      state.value = 'login'
      return
    }

    try {
      const me = await api.me()
      username.value = me.username
      state.value = 'ready'
    } catch (err) {
      if (!(err instanceof UnauthorizedError)) {
        error.value = (err as Error).message
      }
      state.value = 'login'
    }
  }

  async function login(name: string, password: string) {
    busy.value = true
    error.value = ''
    try {
      const result = await api.login(name, password)
      username.value = result.username
      state.value = 'ready'
    } catch (err) {
      error.value = (err as Error).message
      throw err
    } finally {
      busy.value = false
    }
  }

  /** completeSetup 在初始化向导提交成功后调用，此时后端已经把会话种好了。 */
  function completeSetup(name: string) {
    username.value = name
    state.value = 'ready'
  }

  async function logout() {
    busy.value = true
    try {
      await api.logout()
    } catch {
      // 注销失败也要让界面回到登录页：本地已经不该再显示数据了。
    } finally {
      busy.value = false
      username.value = ''
      state.value = 'login'
    }
  }

  return { state, username, error, busy, authenticated, bootstrap, login, logout, completeSetup }
})
