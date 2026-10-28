import type { ProbeQueueItem, ProbeQueueResult, ProbeQueueStatus } from '../api/mediaProbeQueue'

export const probeQueueStatusLabels: Record<ProbeQueueStatus, string> = {
  pending: '待处理', running: '运行中', completed: '已完成', partial: '部分信息未知',
  failed: '失败', canceled: '已取消', interrupted: '已中断',
}

export function probeQueueElapsedLabel(item: ProbeQueueItem, now: number): string {
  if (item.status === 'pending' || item.status === 'canceled') return '—'
  // Restart recovery records when the service noticed the interruption, not
  // when the probe actually stopped, so it cannot provide a reliable duration.
  if (item.status === 'interrupted' || !item.started_at) return '未记录'
  const started = Date.parse(item.started_at)
  const finished = item.status === 'running' ? now : Date.parse(item.finished_at || '')
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) return '未记录'
  const duration = `${((finished - started) / 1000).toFixed(2)} 秒`
  return item.status === 'running' ? `已用 ${duration}` : duration
}

export function probeQueueItemTitle(item: ProbeQueueItem): string {
  const parts = [item.title]
  if (item.episode_num > 0) {
    let episode = `S${String(item.season_num).padStart(2, '0')}E${String(item.episode_num).padStart(2, '0')}`
    if (item.episode_end_num > item.episode_num) episode += `–E${String(item.episode_end_num).padStart(2, '0')}`
    parts.push(episode)
  } else if (item.season_num > 0) {
    parts.push(`第 ${item.season_num} 季 · 集数未知`)
  }
  if (item.episode_title && item.episode_title !== item.title) parts.push(item.episode_title)
  return parts.join(' · ')
}

export function probeQueueFailureLabel(item: ProbeQueueItem): string {
  switch (item.failure_kind) {
    case 'risk_control': return item.provider_code === '40140117' ? '115 请求过频 / 风控' : '上游风控提示'
    case 'rate_limited': return '上游限流'
    case 'access_denied': return '访问被拒绝，未确认风控'
    case 'unauthorized': return '鉴权失败'
    case 'timeout': return '探测超时，未确认风控'
    case 'canceled': return '探测已取消'
    case 'probe_failed': return '探测失败'
    default: return item.error ? '失败原因未分类' : ''
  }
}

export function probeQueueResolution(result: ProbeQueueResult): string {
  if (!(result.width > 0 && result.height > 0)) return '分辨率未知'
  const longEdge = Math.max(result.width, result.height)
  const shortEdge = Math.min(result.width, result.height)
  const label = longEdge >= 7680 && shortEdge >= 2880 ? '8K'
    : longEdge >= 3840 && shortEdge >= 1440 ? '4K'
      : longEdge >= 2560 && shortEdge >= 1440 ? '1440p'
        : longEdge >= 1920 && shortEdge >= 720 ? '1080p'
          : longEdge >= 1280 && shortEdge >= 500 ? '720p' : ''
  return `${label ? `${label} · ` : ''}${result.width}×${result.height}`
}

export function probeQueueResultDetails(result: ProbeQueueResult): string[] {
  const parts: string[] = []
  parts.push(`视频 ${result.video_codec ? result.video_codec.toUpperCase() : '编码未知'}`)
  if (result.video_range) parts.push(result.video_range)
  if (result.video_bit_depth > 0) parts.push(`${result.video_bit_depth} bit`)
  if (result.frame_rate > 0) parts.push(`${Number(result.frame_rate.toFixed(3))} fps`)
  parts.push(`音频 ${result.audio_codec ? result.audio_codec.toUpperCase() : '编码未知'}`)
  if (result.audio_channels > 0) parts.push(`${result.audio_channels} 声道`)
  if (result.audio_sample_rate > 0) parts.push(`${Number((result.audio_sample_rate / 1000).toFixed(1))} kHz`)
  if (result.bit_rate > 0) parts.push(`${Number((result.bit_rate / 1_000_000).toFixed(2))} Mbps`)
  if (result.duration_sec > 0) {
    const seconds = Math.floor(result.duration_sec)
    const hours = Math.floor(seconds / 3600)
    parts.push(`时长 ${hours > 0 ? `${hours}:` : ''}${String(Math.floor(seconds / 60) % 60).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`)
  }
  if (result.container) parts.push(result.container)
  return parts
}
