import assert from 'node:assert/strict'
import test from 'node:test'
import { probeQueueFailureLabel, probeQueueItemTitle, probeQueueResolution, probeQueueResultDetails } from './mediaProbeQueueModel.ts'

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
