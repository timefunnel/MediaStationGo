import { api, LONG_REQUEST_TIMEOUT } from './client'

export type MediaTitleRelation = 'standalone' | 'version' | 'part'

export interface MediaTitleCleanupSuggestion {
  media_id: string
  current_title?: string
  source_directory?: string
  filename?: string
  title: string
  relation: MediaTitleRelation
  group_key?: string
  year?: number
  confidence: number
  reason?: string
}

export interface MediaTitleCleanupPreview {
  library_id: string
  candidate_count: number
  batch_count: number
  remaining_count: number
  suggestions: MediaTitleCleanupSuggestion[]
}

export const titleCleanupAPI = {
  preview: (libraryID: string, groupLimit = 5) =>
    api
      .post<MediaTitleCleanupPreview>(`/libraries/${libraryID}/title-cleanup/preview`, {
        group_limit: groupLimit,
      }, { timeout: LONG_REQUEST_TIMEOUT })
      .then((response) => response.data),

  apply: (libraryID: string, items: MediaTitleCleanupSuggestion[]) =>
    api
      .post<{ updated: number }>(`/libraries/${libraryID}/title-cleanup/apply`, { items })
      .then((response) => response.data),
}
