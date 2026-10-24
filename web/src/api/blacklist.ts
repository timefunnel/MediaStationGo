import { api } from './client'
import type { Media } from '../types'

export interface BlacklistItem {
  id: string
  scope: 'movie' | 'series'
  media: Media | null
}

export const blacklistAPI = {
  list: () => api.get<{ items: BlacklistItem[] }>('/blacklist').then((r) => r.data.items),
  status: (mediaId: string) =>
    api.get<{ blocked: boolean }>(`/media/${mediaId}/blacklist/status`).then((r) => r.data.blocked),
  set: (mediaId: string, blocked: boolean) =>
    (blocked ? api.post(`/media/${mediaId}/blacklist`) : api.delete(`/media/${mediaId}/blacklist`)),
  remove: (id: string) => api.delete(`/blacklist/${id}`),
}
