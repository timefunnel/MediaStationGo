import assert from 'node:assert/strict'
import test from 'node:test'

import {
  cappedResourceTotal,
  cappedResourceTotalPages,
  clampResourcePage,
  isResourceImportCompleted,
  isResourceImportCompletedWithWarning,
  resourceImportDuplicateConflict,
  resourceImportError,
  resourceImportProgress,
  resourceSearchFailure,
  resolveResourceRootID,
  supportsResourceSource,
} from './resourceImportModel.ts'

test('资源搜索最多展示 100 条并限制分页', () => {
  assert.equal(cappedResourceTotal(238), 100)
  assert.equal(cappedResourceTotalPages(238, 20, 12), 5)
  assert.equal(clampResourcePage(9, 5), 5)
  assert.equal(clampResourcePage(0, 5), 1)
})

test('单 root 自动选择，多 root 必须显式选择', () => {
  const roots = [
    { id: 'root-a', path: '/a' },
    { id: 'root-b', path: '/b' },
  ]
  assert.equal(resolveResourceRootID(roots, ''), '')
  assert.equal(resolveResourceRootID([roots[0]], ''), 'root-a')
  assert.equal(resolveResourceRootID(roots, 'root-b'), 'root-b')
})

test('仅在后端声明能力时显示 Pansou 补查', () => {
  assert.equal(supportsResourceSource(undefined, 'pansou'), false)
  assert.equal(supportsResourceSource({ pansou: true }, 'pansou'), true)
  assert.equal(supportsResourceSource({ sources: ['pansou'] }, 'pansou'), true)
})

test('任务进度兼容 0-1 和 0-100 数值', () => {
  assert.equal(resourceImportProgress(0.42), 42)
  assert.equal(resourceImportProgress(76), 76)
  assert.equal(resourceImportProgress(120), 100)
  assert.equal(resourceImportProgress(undefined), null)
})

test('409 duplicate 仅在明确声明时允许强制入库', () => {
  const conflict = resourceImportDuplicateConflict({
    response: {
      status: 409,
      data: { message: '已存在', can_force: false, media_id: 'media-1' },
    },
  })
  assert.deepEqual(conflict, { message: '已存在', can_force: false, media_id: 'media-1' })
  assert.equal(resourceImportDuplicateConflict({ response: { status: 409, data: {} } }), null)
})

test('completed_with_warning 进入完成链路并保留告警状态', () => {
  assert.equal(isResourceImportCompleted('completed_with_warning'), true)
  assert.equal(isResourceImportCompletedWithWarning('completed_with_warning'), true)
})

test('嵌套错误对象提取 message，不渲染对象字符串', () => {
  assert.equal(
    resourceImportDuplicateConflict({
      response: {
        status: 409,
        data: { error: { code: 'duplicate', message: '重复影片', can_force: true } },
      },
    })?.message,
    '重复影片',
  )
  assert.equal(
    resourceImportError({ response: { data: { error: { code: 'failed', message: '真实错误' } } } }, '兜底'),
    '真实错误',
  )
})

test('搜索源失败保留可恢复能力，不把其他错误误判为空结果', () => {
  assert.deepEqual(
    resourceSearchFailure({
      response: {
        data: {
          error: {
            code: 'search_failed',
            message: 'BT4G timed out',
            capabilities: { pansou: true },
          },
        },
      },
    }),
    { code: 'search_failed', message: 'BT4G timed out', capabilities: { pansou: true } },
  )
  assert.equal(resourceSearchFailure({ response: { data: { error: { code: 'unauthorized' } } } }), null)
})
