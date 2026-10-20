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
  updated_at: string
}

export interface ProbeQueueSnapshot {
  config: ProbeQueueConfig
  running: boolean
  counts: Partial<Record<ProbeQueueStatus, number>>
  items: ProbeQueueItem[]
  total: number
  message: string
  collect_error?: string
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
}

const base = '/admin/media-probe-queue'

export const mediaProbeQueueAPI = {
  snapshot: (status: string, page: number) => api.get<ProbeQueueSnapshot>(base, { params: { status, page } }).then((r) => r.data),
  saveConfig: (config: ProbeQueueConfig) => api.put(`${base}/config`, config),
  collect: () => api.post<{ added: number }>(`${base}/collect`).then((r) => r.data),
  plan: () => api.post<ProbeQueuePlan>(`${base}/plan`).then((r) => r.data),
  start: (planID: string) => api.post(`${base}/start`, { plan_id: planID, confirmed: true }),
  pause: () => api.post(`${base}/pause`),
  update: (id: string, action: 'retry' | 'cancel') => api.post(`${base}/${encodeURIComponent(id)}/${action}`),
}
