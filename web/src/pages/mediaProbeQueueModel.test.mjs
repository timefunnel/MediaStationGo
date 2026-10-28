import assert from 'node:assert/strict'
import test from 'node:test'
import { probeQueueElapsedLabel, probeQueueFailureLabel, probeQueueItemTitle, probeQueueResolution, probeQueueResultDetails } from './mediaProbeQueueModel.ts'

const item = { title: 'Synthetic Show', season_num: 2, episode_num: 7, episode_end_num: 0, episode_title: 'Episode Seven' }

test('任务名称细化到季和集，支持特别篇和多集文件', () => {
  assert.equal(probeQueueItemTitle(item), 'Synthetic Show · S02E07 · Episode Seven')
  assert.equal(probeQueueItemTitle({ ...item, season_num: 0, episode_num: 1 }), 'Synthetic Show · S00E01 · Episode Seven')
  assert.equal(probeQueueItemTitle({ ...item, episode_end_num: 8 }), 'Synthetic Show · S02E07–E08 · Episode Seven')
  assert.equal(probeQueueItemTitle({ ...item, season_num: 0, episode_num: 0, episode_title: '' }), 'Synthetic Show')
})

test('分辨率仅使用实际尺寸，包含裁切画幅和未知值', () => {
  assert.equal(probeQueueResolution({ width: 3840, height: 2160 }), '4K · 3840×2160')
  assert.equal(probeQueueResolution({ width: 3840, height: 1600 }), '4K · 3840×1600')
  assert.equal(probeQueueResolution({ width: 1920, height: 800 }), '1080p · 1920×800')
  assert.equal(probeQueueResolution({ width: 2560, height: 1080 }), '1080p · 2560×1080')
  assert.equal(probeQueueResolution({ width: 1280, height: 720 }), '720p · 1280×720')
  assert.equal(probeQueueResolution({ width: 0, height: 0 }), '分辨率未知')
})

test('展示实测编解码、帧率、码率、音频和时长，未知数据不伪造', () => {
  assert.deepEqual(probeQueueResultDetails({ video_codec: 'hevc', audio_codec: 'eac3', video_range: 'HDR10', video_bit_depth: 10, frame_rate: 23.976, audio_channels: 6, audio_sample_rate: 48000, bit_rate: 24000000, duration_sec: 1440, container: 'matroska' }), ['视频 HEVC', 'HDR10', '10 bit', '23.976 fps', '音频 EAC3', '6 声道', '48 kHz', '24 Mbps', '时长 24:00', 'matroska'])
  assert.deepEqual(probeQueueResultDetails({ video_codec: '', audio_codec: '' }), ['视频 编码未知', '音频 编码未知'])
})

test('风控、限流、403和超时提示明确区分', () => {
  assert.equal(probeQueueFailureLabel({ failure_kind: 'risk_control', provider_code: '40140117' }), '115 请求过频 / 风控')
  assert.equal(probeQueueFailureLabel({ failure_kind: 'rate_limited' }), '上游限流')
  assert.equal(probeQueueFailureLabel({ failure_kind: 'timeout' }), '探测超时，未确认风控')
  assert.equal(probeQueueFailureLabel({ failure_kind: 'access_denied' }), '访问被拒绝，未确认风控')
})

const timedItem = { started_at: '2026-10-28T00:00:00.000Z', finished_at: '2026-10-28T00:00:30.061Z' }

test('已结束任务只用记录的起止时间，刷新页面不会继续增长', () => {
  for (const status of ['completed', 'partial', 'failed']) {
    assert.equal(probeQueueElapsedLabel({ ...timedItem, status }, Date.parse('2026-10-28T01:00:00Z')), '30.06 秒')
  }
  assert.equal(probeQueueElapsedLabel({ status: 'completed', started_at: '2026-10-28T08:00:00+08:00', finished_at: '2026-10-28T00:00:06.044Z' }, 0), '6.04 秒')
})

test('运行中按当前时间显示已用耗时，新一轮只计算新的开始时间', () => {
  const running = { status: 'running', started_at: timedItem.started_at }
  assert.equal(probeQueueElapsedLabel(running, Date.parse('2026-10-28T00:00:08.125Z')), '已用 8.13 秒')
  assert.equal(probeQueueElapsedLabel({ ...running, started_at: '2026-10-28T00:00:08Z' }, Date.parse('2026-10-28T00:00:09Z')), '已用 1.00 秒')
})

test('缺失、无效、倒置时间和重启中断不伪造耗时，未执行任务不使用上次时间', () => {
  for (const item of [
    { status: 'completed', finished_at: timedItem.finished_at },
    { status: 'failed', started_at: timedItem.started_at },
    { ...timedItem, status: 'completed', started_at: 'invalid' },
    { ...timedItem, status: 'failed', finished_at: '2026-10-27T23:00:00Z' },
    { ...timedItem, status: 'interrupted' },
  ]) assert.equal(probeQueueElapsedLabel(item, Date.now()), '未记录')
  for (const status of ['pending', 'canceled']) assert.equal(probeQueueElapsedLabel({ ...timedItem, status }, Date.now()), '—')
})
