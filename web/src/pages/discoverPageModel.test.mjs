import assert from 'node:assert/strict'
import test from 'node:test'

import { orderSelectedSections } from './discoverPageModel.ts'

const sections = [
  { key: 'first', label: '第一模块', provider: 'test' },
  { key: 'second', label: '第二模块', provider: 'test' },
  { key: 'third', label: '第三模块', provider: 'test' },
]

test('发现模块始终按分区定义顺序展示', () => {
  assert.deepEqual(orderSelectedSections(['third', 'first'], sections), ['first', 'third'])
  assert.deepEqual(orderSelectedSections(['third', 'first', 'second'], sections), ['first', 'second', 'third'])
})
