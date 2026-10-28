import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import * as React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import * as jsx from 'react/jsx-runtime'
import ts from 'typescript'
import * as model from './mediaProbeQueueModel.ts'

const code = ts.transpileModule(readFileSync(new URL('./MediaProbeQueueTable.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText
const component = {}
new Function('require', 'exports', code)((name) => {
  if (name === 'react') return React
  if (name === 'react/jsx-runtime') return jsx
  if (name === './mediaProbeQueueModel') return model
  if (name === 'react-router-dom') return { Link: ({ to, children, ...props }) => React.createElement('a', { href: to, ...props }, children) }
  throw new Error(`Unexpected dependency ${name}`)
}, component)

const base = {
  media_id: 'synthetic-episode', title: 'Synthetic Show', season_num: 2, episode_num: 7, episode_end_num: 0,
  episode_title: 'Episode Seven', file_name: 'Show.S02E07.1080p.mkv', status: 'completed', attempts: 17, updated_at: '2026-10-27T12:00:00Z',
}
const render = (item, disabled = false) => renderToStaticMarkup(React.createElement(component.MediaProbeQueueTable, {
  items: [item], disabled, onRetry() {}, onRequeue() {}, onCancel() {},
}))

test('表格显示单集、实际探测分辨率和编码，隐藏次数', () => {
  const html = render({ ...base, result: { width: 3840, height: 1600, video_codec: 'hevc', audio_codec: 'aac', frame_rate: 23.976 } })
  assert.match(html, /S02E07/)
  assert.match(html, /Episode Seven/)
  assert.match(html, /4K · 3840×1600/)
  assert.match(html, /HEVC/)
  assert.match(html, /23.976 fps/)
  assert.match(html, /Show.S02E07.1080p.mkv/)
  assert.doesNotMatch(html, /状态 \/ 次数|已完成 · 17/)
})

test('失败记录显示风控证据与单条重试入口，忙碌时禁用', () => {
  const html = render({ ...base, status: 'failed', failure_kind: 'risk_control', provider_code: '40140117', error: '访问过于频繁' }, true)
  assert.match(html, /115 请求过频 \/ 风控/)
  assert.match(html, /上游码 40140117/)
  assert.match(html, /访问过于频繁/)
  assert.match(html, /disabled=""[^>]*>重试此项/)
  assert.doesNotMatch(html, /重新入队/)
})

test('没有探测结果时不会按文件名中的1080p生成参数', () => {
  const html = render(base)
  assert.match(html, /没有已保存的探测参数/)
  assert.doesNotMatch(html, /1080p ·|1920×1080|已保存的探测参数<\/p>/)
})

test('每条已执行记录展示实际耗时，失败也保留耗时', () => {
  const timestamps = { started_at: '2026-10-28T00:00:00.000Z', finished_at: '2026-10-28T00:00:06.044Z' }
  for (const status of ['completed', 'partial', 'failed']) {
    const html = render({ ...base, ...timestamps, status })
    assert.match(html, /实际耗时/)
    assert.match(html, />6\.04 秒<\/td>/)
  }
})

test('历史记录没有开始时间时显示未记录，不借更新时间猜耗时', () => {
  assert.match(render({ ...base, finished_at: '2026-10-28T00:00:06Z' }), />未记录<\/td>/)
})
