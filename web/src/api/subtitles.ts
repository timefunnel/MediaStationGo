import { api } from './client'
import { useAuthStore } from '../stores/auth'

export interface SubtitleTrack {
  lang: string
  label: string
  name: string
  path: string
  url: string
  codec: string
  source: string
}

export interface SubtitleSearchCandidate {
  candidate_id: string
  provider: string
  title: string
  filename: string
  language: string
  source_score: number
  rank: number
}

export interface SubtitleSearchResponse {
  session_id: string
  expires_at: number
  media_id: string
  title: string
  category: string
  query: string
  items: SubtitleSearchCandidate[]
}

export interface SubtitleCandidatePreview extends SubtitleSearchCandidate {
  media_id: string
  content_sample: string
  preview_char_count: number
  preview_line_count: number
}

export interface SubtitleApplyResult {
  media_id: string
  status: string
  source: string
  filename: string
  count: number
  reason: string
}

export const subtitlesAPI = {
  list: (mediaId: string) =>
    api
      .get<{ tracks: SubtitleTrack[] | null }>(`/media/${mediaId}/subtitles`)
      .then((r) => r.data.tracks ?? []),

  previewExisting: (mediaId: string, path: string) =>
    api
      .get<string>(`/subtitles/${encodeURIComponent(mediaId)}`, {
        params: { path },
        responseType: 'text',
      })
      .then((r) => r.data),

  delete: (mediaId: string, path: string) =>
    api
      .delete<{ deleted: boolean }>(`/media/${encodeURIComponent(mediaId)}/subtitles`, { params: { path } })
      .then((r) => r.data),

  search: (mediaId: string, limit = 20) =>
    api
      .post<SubtitleSearchResponse>(`/media/${encodeURIComponent(mediaId)}/subtitles/search`, { limit })
      .then((r) => r.data),

  previewCandidate: (mediaId: string, searchSessionId: string, candidateId: string) =>
    api
      .post<SubtitleCandidatePreview>(`/media/${encodeURIComponent(mediaId)}/subtitles/preview`, {
        search_session_id: searchSessionId,
        candidate_id: candidateId,
      })
      .then((r) => r.data),

  applyCandidate: (mediaId: string, searchSessionId: string, candidateId: string) =>
    api
      .post<{ result: SubtitleApplyResult; tracks: SubtitleTrack[] }>(
        `/media/${encodeURIComponent(mediaId)}/subtitles/apply`,
        { search_session_id: searchSessionId, candidate_id: candidateId },
      )
      .then((r) => r.data),

  url: (mediaId: string, path: string) => {
    const token = useAuthStore.getState().token ?? ''
    return `/api/subtitles/${encodeURIComponent(mediaId)}?path=${encodeURIComponent(
      path,
    )}&token=${encodeURIComponent(token)}`
  },
}
