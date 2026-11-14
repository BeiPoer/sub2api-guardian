import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, ref, watch } from 'vue'

function compile(source) {
  return ts.transpileModule(source.replace(/^import .*$/gm, '').replace('export const ', 'const '), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None },
  }).outputText
}
const storeScript = compile(readFileSync(new URL('../src/stores/guardian.ts', import.meta.url), 'utf8') + '\nglobalThis.store = useGuardianStore();')
const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }

function setup() {
  let now = 0, nextID = 0
  const timers = new Map(), listeners = new Map(), streams = [], calls = []
  const document = {
    hidden: false,
    addEventListener(event, callback) { listeners.set(event, callback) },
    removeEventListener(event) { listeners.delete(event) },
  }
  const window = {
    setTimeout(fn, delay) { const id = ++nextID; timers.set(id, { fn, at: now + delay }); return id },
    clearTimeout(id) { timers.delete(id) },
    setInterval(fn, delay) { const id = ++nextID; timers.set(id, { fn, at: now + delay, delay }); return id },
    clearInterval(id) { timers.delete(id) },
  }
  class EventSource {
    handlers = {}
    closed = false
    constructor() { streams.push(this) }
    addEventListener(name, fn) { this.handlers[name] = fn }
    close() { this.closed = true }
  }
  const context = {
    ref, computed, defineStore: (_, factory) => factory, document, window, EventSource,
    Date: class extends Date { static now() { return now } },
    api: {
      async overview() { calls.push('overview'); return { groups: [], status: {} } },
      async channels() { calls.push('channels'); return { items: [] } },
    },
  }
  runInNewContext(storeScript, context)
  return {
    store: context.store, streams, calls, timers, listeners, document,
    event(name) { listeners.get(name)?.() },
    async advance(ms) {
      const end = now + ms
      while (true) {
        const next = [...timers].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0]
        if (!next) break
        const [id, timer] = next
        now = timer.at
        if (timer.delay) timer.at += timer.delay
        else timers.delete(id)
        timer.fn()
        await settle()
      }
      now = end
    },
  }
}

test('SSE 正常不轮询，断线兜底，5 分钟空闲后停止所有全局自动请求', async () => {
  const f = setup()
  f.store.startAutoUpdate()
  await settle()
  assert.equal(f.calls.length, 2)
  f.streams[0].onopen()
  await f.advance(20_000)
  assert.equal(f.calls.length, 2)
  f.streams[0].handlers.tick()
  await settle()
  assert.equal(f.calls.length, 4)
  f.streams[0].onerror()
  await f.advance(20_000)
  assert.equal(f.calls.length, 6)
  await f.advance(260_000)
  assert.equal(f.store.autoUpdating.value, false)
  assert.equal(f.streams[0].closed, true)
  assert.equal(f.timers.size, 0)
  const count = f.calls.length
  f.streams[0].handlers.tick()
  f.streams[0].onopen()
  await f.advance(60_000)
  assert.equal(f.calls.length, count)
  assert.equal(f.store.connected.value, false)
})

test('隐藏立即断开，返回仅补刷一次，点击键盘滚动续期，普通鼠标移动不续期', async () => {
  const f = setup()
  f.store.startAutoUpdate()
  await settle()
  f.document.hidden = true
  f.event('visibilitychange')
  assert.equal(f.store.autoUpdating.value, false)
  assert.equal(f.timers.size, 0)
  await f.advance(600_000)
  assert.equal(f.calls.length, 2)
  f.document.hidden = false
  f.event('visibilitychange')
  f.event('click')
  f.event('keydown')
  await settle()
  assert.equal(f.calls.length, 4)
  assert.equal(f.streams.length, 2)
  f.streams[1].onopen()
  for (const event of ['click', 'keydown', 'scroll']) {
    await f.advance(180_000)
    f.event(event)
    await f.advance(60_000)
    assert.equal(f.store.autoUpdating.value, true)
  }
  f.event('mousemove')
  await f.advance(240_000)
  assert.equal(f.store.autoUpdating.value, false)
  assert.equal(f.calls.length, 4)
  f.event('click')
  await settle()
  assert.equal(f.store.autoUpdating.value, true)
  assert.equal(f.calls.length, 6)
  f.store.reset()
  assert.equal(f.listeners.size, 0)
  assert.equal(f.timers.size, 0)
  f.event('click')
  assert.equal(f.store.autoUpdating.value, false)
})

test('隐藏时登录不自动拉取，退出后不会被旧请求重新连接', async () => {
  const f = setup()
  f.document.hidden = true
  f.store.startAutoUpdate()
  assert.equal(f.calls.length, 0)
  f.document.hidden = false
  f.event('visibilitychange')
  f.store.reset()
  await settle()
  assert.equal(f.store.overview.value, null)
  assert.equal(f.streams[0].closed, true)
  assert.equal(f.timers.size, 0)
})

test('模型检测暂停时清理定时器，恢复不重叠轮询，暂停中的请求结束后不继续刷新历史', async () => {
  const source = readFileSync(new URL('../src/views/ModelCheckView.vue', import.meta.url), 'utf8').split('<script setup lang="ts">')[1].split('</script>')[0]
  const script = compile(source + '\nglobalThis.view = { poll, historyOpen, loading, loadError }; initialized = true;')
  const active = ref(true), timers = new Map(), calls = []
  let release
  const guardian = { get autoUpdating() { return active.value }, canAutoUpdate: () => active.value }
  const stops = []
  const context = {
    computed, ref, watch: (...args) => stops.push(watch(...args)),
    onMounted() {}, onBeforeUnmount() {}, useUIStore: () => ({}), useGuardianStore: () => guardian,
    setTimeout(fn) { timers.set(1, fn); return 1 }, clearTimeout(id) { timers.delete(id) },
    api: {
      modelCheck() { calls.push('view'); return new Promise(resolve => { release = () => resolve({ running_targets: [] }) }) },
      async modelCheckTimelines() { calls.push('timeline'); return { items: [] } },
      async modelCheckRuns() { calls.push('history'); return { items: [] } },
    },
  }
  runInNewContext(script, context)
  context.view.loading.value = false
  context.view.historyOpen.value = true
  const pending = context.view.poll()
  active.value = false
  active.value = true
  assert.deepEqual(calls, ['view', 'timeline'])
  active.value = false
  release()
  await pending
  assert.equal(timers.size, 0)
  assert.equal(calls.includes('history'), false)
  context.view.historyOpen.value = false
  active.value = true
  release()
  await settle()
  assert.equal(timers.size, 1)
  active.value = false
  assert.equal(timers.size, 0)
  stops.forEach(stop => stop())
})
