import assert from 'node:assert/strict'
import test from 'node:test'

import { discoverCardMetaText, orderSelectedSections } from './discoverPageModel.ts'

const sections = [
  { key: 'first', label: '第一模块', provider: 'test' },
  { key: 'second', label: '第二模块', provider: 'test' },
  { key: 'third', label: '第三模块', provider: 'test' },
]

test('发现模块始终按分区定义顺序展示', () => {
  assert.deepEqual(orderSelectedSections(['third', 'first'], sections), ['first', 'third'])
  assert.deepEqual(orderSelectedSections(['third', 'first', 'second'], sections), ['first', 'second', 'third'])
})

test('发现卡片展示完整发行日期而不是年份', () => {
  assert.equal(discoverCardMetaText({ title: '作品', media_type: 'adult', release_date: '2026-08-04', year: 2026 }), 'adult · 2026-08-04')
  assert.equal(discoverCardMetaText({ title: '作品', media_type: 'movie', year: 2026 }), 'movie')
})
