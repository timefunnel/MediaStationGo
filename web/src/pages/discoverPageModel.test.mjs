import assert from 'node:assert/strict'
import test from 'node:test'

import { discoverCardMetaText, orderSelectedSections } from './discoverPageModel.ts'

const sections = [
  { key: 'first', label: '第一模块', provider: 'test' },
  { key: 'second', label: '第二模块', provider: 'test' },
  { key: 'third', label: '第三模块', provider: 'test' },
]

test('发现模块保留用户排序并过滤无效或重复模块', () => {
  assert.deepEqual(orderSelectedSections(['third', 'first'], sections), ['third', 'first'])
  assert.deepEqual(
    orderSelectedSections(['third', 'missing', 'first', 'third', 'second'], sections),
    ['third', 'first', 'second'],
  )
})

test('发现卡片展示完整发行日期而不是年份', () => {
  assert.equal(discoverCardMetaText({ title: '作品', media_type: 'adult', release_date: '2026-08-04', year: 2026 }), 'adult · 2026-08-04')
  assert.equal(discoverCardMetaText({ title: '作品', media_type: 'movie', year: 2026 }), 'movie')
})
