import { api } from './client'

export type ProbeQueueStatus = 'pending' | 'running' | 'completed' | 'partial' | 'failed' | 'canceled' | 'interrupted'

export interface ProbeQueueConfig {
  auto_collect: boolean
  library_ids: string[]
  interval_seconds: number
  timeout_seconds: number
  batch_size: number
  run_seconds: number
}

export interface ProbeQueueItem {
  media_id: string
  title: string
  library_id: string
  status: ProbeQueueStatus
  attempts: number
  error?: string
  failure_kind?: 'risk_control' | 'rate_limited' | 'access_denied' | 'unauthorized' | 'timeout' | 'canceled' | 'probe_failed'
  http_status?: number
  provider_code?: string
  season_num: number
  episode_num: number
  episode_end_num: number
  episode_title?: string
  file_name?: string
  finished_at?: string
  result?: ProbeQueueResult
  updated_at: string
}

export interface ProbeQueueResult {
  duration_sec: number
  width: number
  height: number
  video_codec: string
  audio_codec: string
  container: string
  bit_rate: number
  frame_rate: number
  video_range: string
  video_bit_depth: number
  audio_channels: number
  audio_sample_rate: number
}

export interface ProbeQueueSnapshot {
  config: ProbeQueueConfig
  running: boolean
  counts: Partial<Record<ProbeQueueStatus, number>>
  items: ProbeQueueItem[]
  total: number
  message: string
  collect_error?: string
  risk_alert?: ProbeQueueItem
}

export interface ProbeQueuePlan {
  id: string
  expires_at: string
  config: ProbeQueueConfig
  count: number
  local_count: number
  openlist_count: number
  remote_count: number
  concurrency: number
  targets: string[]
  notice: string
  retry_media_id?: string
}

const base = '/admin/media-probe-queue'

export const mediaProbeQueueAPI = {
  snapshot: (status: string, page: number) => api.get<ProbeQueueSnapshot>(base, { params: { status, page } }).then((r) => r.data),
  saveConfig: (config: ProbeQueueConfig) => api.put(`${base}/config`, config),
  collect: () => api.post<{ added: number }>(`${base}/collect`).then((r) => r.data),
  plan: () => api.post<ProbeQueuePlan>(`${base}/plan`).then((r) => r.data),
  retryPlan: (id: string) => api.post<ProbeQueuePlan>(`${base}/${encodeURIComponent(id)}/retry-plan`).then((r) => r.data),
  start: (planID: string) => api.post(`${base}/start`, { plan_id: planID, confirmed: true }),
  pause: () => api.post(`${base}/pause`),
  update: (id: string, action: 'retry' | 'cancel') => api.post(`${base}/${encodeURIComponent(id)}/${action}`),
}
