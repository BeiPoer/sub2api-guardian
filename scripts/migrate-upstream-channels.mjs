#!/usr/bin/env node

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(scriptDir, '..')
const defaultSource = path.resolve(repoRoot, '..', 'ai-channel-manager', 'data', 'app.sqlite')
const defaultTarget = path.join(repoRoot, 'backend', 'data', 'guardian.sqlite')

function usage() {
  console.log(`
旧项目渠道迁移工具

默认只预览，不写入数据库。确认预览结果后再加 --apply。

用法：
  node scripts/migrate-upstream-channels.mjs
  node scripts/migrate-upstream-channels.mjs --apply
  node scripts/migrate-upstream-channels.mjs --source <旧 app.sqlite> --target <新 guardian.sqlite> --apply

选项：
  --source <path>       旧项目数据库，默认 ../ai-channel-manager/data/app.sqlite
  --target <path>       Guardian 数据库，默认 backend/data/guardian.sqlite
  --apply               自动备份目标库后执行迁移
  --assume-active       旧库没有 ignored 列时，将所有渠道按未忽略处理
  -h, --help            显示帮助

迁移内容：渠道名称、类型、地址、账号密码、New API 凭据和 ignored 状态。
不会迁移余额历史、查询日志、缓存、自动任务、任务基线或告警事件。
`)
}

function parseArgs(argv) {
  const options = {
    source: defaultSource,
    target: defaultTarget,
    apply: false,
    assumeActive: false,
    help: false
  }

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '-h' || arg === '--help') {
      options.help = true
      continue
    }
    if (arg === '--apply') {
      options.apply = true
      continue
    }
    if (arg === '--assume-active') {
      options.assumeActive = true
      continue
    }
    if (arg === '--source' || arg === '--target') {
      const value = argv[index + 1]
      if (!value || value.startsWith('--')) throw new Error(`${arg} 缺少路径`)
      options[arg === '--source' ? 'source' : 'target'] = value
      index += 1
      continue
    }
    if (arg.startsWith('--source=')) {
      options.source = arg.slice('--source='.length)
      continue
    }
    if (arg.startsWith('--target=')) {
      options.target = arg.slice('--target='.length)
      continue
    }
    throw new Error(`未知选项：${arg}`)
  }
  return options
}

function quoteIdentifier(value) {
  return `"${value.replaceAll('"', '""')}"`
}

function tableExists(db, table) {
  return Boolean(db.prepare("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?").get(table))
}

function tableColumns(db, table) {
  return new Set(db.prepare(`PRAGMA table_info(${quoteIdentifier(table)})`).all().map(row => String(row.name)))
}

function stringValue(value) {
  return value === null || value === undefined ? '' : String(value)
}

function trimmed(value) {
  return stringValue(value).trim()
}

function booleanValue(value) {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  return ['1', 'true', 'yes', 'on'].includes(trimmed(value).toLowerCase())
}

function canonicalPart(value) {
  return trimmed(value).toLowerCase()
}

function channelKey(channel) {
  return [
    canonicalPart(channel.type),
    canonicalPart(channel.baseURL).replace(/\/+$/, ''),
    canonicalPart(channel.name),
    canonicalPart(channel.username),
    canonicalPart(channel.newAPIUserID)
  ].join('\u001f')
}

function timestamp(value, fallback) {
  const candidate = trimmed(value)
  return candidate || fallback
}

function normalizeChannel(row, hasIgnored) {
  const type = trimmed(row.type)
  if (!['sub2api', 'newapi', 'other'].includes(type)) {
    throw new Error(`源库渠道 ${row.id ?? '?'} 的类型无效：${type || '(空)'}`)
  }

  const channel = {
    sourceID: Number(row.id),
    name: trimmed(row.name),
    type,
    baseURL: trimmed(row.base_url),
    username: stringValue(row.username),
    password: stringValue(row.password),
    newAPIAccessToken: stringValue(row.newapi_access_token),
    newAPIUserID: stringValue(row.newapi_user_id),
    ignored: hasIgnored ? booleanValue(row.ignored) : false,
    createdAt: trimmed(row.created_at)
  }

  if (!channel.name) throw new Error(`源库渠道 ${row.id ?? '?'} 缺少名称`)
  if (!channel.baseURL) throw new Error(`源库渠道 ${row.id ?? '?'} 缺少站点地址`)
  return channel
}

function readSource(sourcePath) {
  const db = new DatabaseSync(sourcePath, { readOnly: true })
  try {
    if (!tableExists(db, 'channels')) throw new Error('源数据库不存在 channels 表')
    const columns = tableColumns(db, 'channels')
    const hasIgnored = columns.has('ignored')
    const fields = [
      'id', 'name', 'type', 'base_url', 'username', 'password',
      'newapi_access_token', 'newapi_user_id', 'ignored', 'created_at'
    ].filter(field => columns.has(field))
    const rows = db.prepare(`SELECT ${fields.map(quoteIdentifier).join(', ')} FROM "channels" ORDER BY "id"`).all()
    return { hasIgnored, rows: rows.map(row => normalizeChannel(row, hasIgnored)) }
  } finally {
    db.close()
  }
}

function readTarget(targetPath) {
  const db = new DatabaseSync(targetPath, { readOnly: true })
  try {
    if (!tableExists(db, 'upstream_channels')) throw new Error('目标数据库不存在 upstream_channels 表，请先启动 Guardian 初始化数据库')
    const rows = db.prepare(`
      SELECT id, name, type, base_url, username, newapi_user_id, created_at
      FROM upstream_channels
      ORDER BY id
    `).all()
    return rows
  } finally {
    db.close()
  }
}

function buildPlan(sourceRows, targetRows) {
  const existing = new Map(targetRows.map(row => [channelKey({
    type: row.type,
    name: row.name,
    baseURL: row.base_url,
    username: row.username,
    newAPIUserID: row.newapi_user_id
  }), row]))
  const seen = new Set()
  const plan = []
  let duplicateSource = 0

  for (const channel of sourceRows) {
    const key = channelKey(channel)
    if (seen.has(key)) {
      duplicateSource += 1
      continue
    }
    seen.add(key)
    plan.push({ channel, existing: existing.get(key) || null })
  }
  return { plan, duplicateSource }
}

function printPlan(sourcePath, targetPath, source, plan, duplicateSource) {
  const counts = new Map()
  for (const item of plan) {
    const key = `${item.channel.type}/${item.channel.ignored ? 'ignored' : 'active'}`
    counts.set(key, (counts.get(key) || 0) + 1)
  }
  const added = plan.filter(item => !item.existing).length
  const updated = plan.length - added

  console.log(`源数据库：${sourcePath}`)
  console.log(`目标数据库：${targetPath}`)
  console.log(`源渠道：${source.rows.length} 条`)
  for (const type of ['sub2api', 'newapi', 'other']) {
    console.log(`  ${type} 正常 ${counts.get(`${type}/active`) || 0} 条，忽略 ${counts.get(`${type}/ignored`) || 0} 条`)
  }
  console.log(`将新增：${added} 条，更新同渠道：${updated} 条`)
  if (duplicateSource) console.log(`源库重复记录：${duplicateSource} 条（将跳过完全相同的渠道）`)
  if (!source.hasIgnored) {
    console.warn('警告：源库没有 ignored 列，当前预览按“全部未忽略”处理。')
  }
}

function backupTarget(targetPath) {
  const stamp = new Date().toISOString().replaceAll('-', '').replaceAll(':', '').replace(/\.\d{3}Z$/, 'Z')
  const backupPath = `${targetPath}.before-upstream-${stamp}.bak`
  fs.copyFileSync(targetPath, backupPath)
  for (const suffix of ['-wal', '-shm']) {
    const sidecar = `${targetPath}${suffix}`
    if (fs.existsSync(sidecar)) fs.copyFileSync(sidecar, `${backupPath}${suffix}`)
  }
  return backupPath
}

function applyPlan(targetPath, plan) {
  const db = new DatabaseSync(targetPath)
  const now = new Date().toISOString()
  const insert = db.prepare(`
    INSERT INTO upstream_channels (
      name, type, base_url, username, password, newapi_access_token, newapi_user_id,
      sub2api_access_token, sub2api_refresh_token, sub2api_token_expires_at,
      ignored, status, last_sync_at, last_error, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', NULL, ?, 'active', NULL, '', ?, ?)
  `)
  const update = db.prepare(`
    UPDATE upstream_channels SET
      name = ?, type = ?, base_url = ?, username = ?, password = ?,
      newapi_access_token = ?, newapi_user_id = ?,
      sub2api_access_token = '', sub2api_refresh_token = '', sub2api_token_expires_at = NULL,
      ignored = ?, status = 'active', last_sync_at = NULL, last_error = '', updated_at = ?
    WHERE id = ?
  `)

  db.exec('PRAGMA foreign_keys = ON;')
  db.exec('BEGIN IMMEDIATE;')
  try {
    const mapping = []
    for (const { channel, existing } of plan) {
      const values = [
        channel.name,
        channel.type,
        channel.baseURL,
        channel.username,
        channel.password,
        channel.newAPIAccessToken,
        channel.newAPIUserID,
        channel.ignored ? 1 : 0,
        timestamp(channel.createdAt, now),
        now
      ]
      let targetID
      if (existing) {
        update.run(...values.slice(0, 8), values[9], existing.id)
        targetID = existing.id
      } else {
        const result = insert.run(...values)
        targetID = Number(result.lastInsertRowid)
      }
      mapping.push(`${channel.sourceID || '?'}→${targetID}`)
    }
    db.exec('COMMIT;')
    return mapping
  } catch (error) {
    try { db.exec('ROLLBACK;') } catch { /* 保留原始错误 */ }
    throw error
  } finally {
    db.close()
  }
}

function printTargetCounts(targetPath) {
  const db = new DatabaseSync(targetPath, { readOnly: true })
  try {
    const rows = db.prepare(`
      SELECT type, ignored, COUNT(*) AS count
      FROM upstream_channels
      GROUP BY type, ignored
      ORDER BY type, ignored
    `).all()
    console.log('目标库当前渠道分布：')
    for (const row of rows) console.log(`  ${row.type} ${row.ignored ? '忽略' : '正常'}：${row.count} 条`)
  } finally {
    db.close()
  }
}

function main() {
  const options = parseArgs(process.argv.slice(2))
  if (options.help) {
    usage()
    return
  }

  const sourcePath = path.resolve(options.source)
  const targetPath = path.resolve(options.target)
  if (sourcePath === targetPath) throw new Error('源数据库和目标数据库不能是同一个文件')
  if (!fs.existsSync(sourcePath)) throw new Error(`找不到源数据库：${sourcePath}`)
  if (!fs.existsSync(targetPath)) throw new Error(`找不到目标数据库：${targetPath}，请先启动 Guardian 一次`)

  const source = readSource(sourcePath)
  const targetRows = readTarget(targetPath)
  const { plan, duplicateSource } = buildPlan(source.rows, targetRows)
  printPlan(sourcePath, targetPath, source, plan, duplicateSource)

  if (!options.apply) {
    console.log('\n当前是预览模式，没有修改任何数据库。确认后加 --apply 执行。')
    return
  }
  if (!source.hasIgnored && !options.assumeActive) {
    throw new Error('源库没有 ignored 列，无法可靠区分忽略渠道；确认全部应视为正常时再加 --assume-active')
  }
  if (!plan.length) {
    console.log('没有需要迁移的渠道。')
    return
  }

  const backupPath = backupTarget(targetPath)
  const mapping = applyPlan(targetPath, plan)
  console.log(`\n迁移完成，备份文件：${backupPath}`)
  console.log(`旧 ID → 新 ID：${mapping.join('，')}`)
  printTargetCounts(targetPath)
  console.log('余额历史、查询日志、缓存、自动任务、任务基线和告警事件均未复制。')
  console.log('请在 Guardian 的“渠道汇总”页面点击“刷新全部”，确认非忽略渠道状态。')
}

try {
  main()
} catch (error) {
  console.error(`\n迁移失败：${error instanceof Error ? error.message : String(error)}`)
  process.exitCode = 1
}
