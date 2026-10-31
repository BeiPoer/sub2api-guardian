import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, ref } from 'vue'

// Exercise the component's actual selection logic without mounting or making upstream requests.
const source = readFileSync(new URL('../src/views/ModelCheckView.vue', import.meta.url), 'utf8')
  .split('<script setup lang="ts">')[1].split('</script>')[0].replace(/^import .*$/gm, '')
const script = ts.transpileModule(`${source}\nglobalThis.view = { settings, channels, bankModels, groupID, schedulableFilter, search, bulkModels, bulkModelOptions, bulkMessage, bulkLoading, loadBulkModels, selectBulkModels, toggleModels };`, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.None },
}).outputText

function setup(channelModels) {
  const context = { computed, ref, api: { channelModels }, useUIStore: () => ({ notify() {} }), watch() {}, onMounted() {}, onBeforeUnmount() {} }
  runInNewContext(script, context)
  return context.view
}
const channel = (id, extra = {}) => ({ id, name: `供应商 ${id}`, type: 'apikey', schedulable: true, groups: [{ id: 1 }], ...extra })

test('批量选择遵循筛选，跳过不支持或失败渠道，保留已有预期模型并去重', async () => {
  const calls = []
  const view = setup(async id => {
    calls.push(id)
    if (id === 3) throw new Error('模型接口失败')
    return { models: id === 1 ? ['sol', 'sol', 'terra'] : ['sol', 'other'] }
  })
  view.channels.value = [channel(1), channel(2), channel(3), channel(4, { type: 'oauth' }), channel(5, { groups: [{ id: 2 }] }), channel(6, { schedulable: false }), channel(7, { name: '不匹配' })]
  view.groupID.value = 1
  view.schedulableFilter.value = 'enabled'
  view.search.value = '供应商'
  view.bankModels.value = ['sol', 'terra']
  view.bulkModels.value = ['sol', 'terra']
  view.settings.value.targets = [{ account_id: 1, group_id: 1, model: 'sol', expected_model: 'custom-expected' }]
  await view.selectBulkModels()
  assert.deepEqual(calls.sort(), [1, 2, 3])
  assert.deepEqual(JSON.parse(JSON.stringify(view.settings.value.targets)), [
    { account_id: 1, group_id: 1, model: 'sol', expected_model: 'custom-expected' },
    { account_id: 1, group_id: 1, model: 'terra', expected_model: 'terra' },
    { account_id: 2, group_id: 1, model: 'sol', expected_model: 'sol' },
  ])
  assert.match(view.bulkMessage.value, /新增 2 个检测项/)
  assert.match(view.bulkMessage.value, /供应商 3/)
  await view.selectBulkModels()
  assert.equal(view.settings.value.targets.length, 3)
  assert.match(view.bulkMessage.value, /新增 0 个检测项/)
  assert.equal(view.bulkLoading.value, false)
})

test('读取模型只更新选项，并发不超过四个，读取期间固定筛选范围与分组', async () => {
  let active = 0
  let peak = 0
  const view = setup(async id => {
    peak = Math.max(peak, ++active)
    await new Promise(resolve => setTimeout(resolve, 5))
    active--
    return { models: [`alias-${id}`] }
  })
  view.channels.value = Array.from({ length: 6 }, (_, index) => channel(index + 1))
  view.groupID.value = 1
  await view.loadBulkModels()
  assert.equal(peak, 4)
  assert.equal(view.settings.value.targets.length, 0)
  assert.ok(view.bulkModelOptions.value.includes('alias-6'))
  view.bulkModels.value = ['alias-6']
  const selecting = view.selectBulkModels()
  view.groupID.value = 2
  await selecting
  assert.equal(view.settings.value.targets.length, 1)
  assert.equal(view.settings.value.targets[0].account_id, 6)
  assert.equal(view.settings.value.targets[0].group_id, 1)
  assert.equal(view.settings.value.targets[0].expected_model, '')
})
