import { api, LONG_REQUEST_TIMEOUT } from './client'

export interface ProbeExchangeChange {
  media_id: string
  relative_path: string
  unknown_fields?: string[]
  estimated_fields?: string[]
  status: string
  error?: string
  before?: Record<string, number | string>
  after?: Record<string, number | string>
}

export interface ProbeExchangePreview {
  digest: string
  already_applied: boolean
  counts: Record<string, number>
  changes: ProbeExchangeChange[]
}

const base = '/admin/media-probe-queue/exchange'
export const mediaProbeExchangeAPI = {
  export: (libraryIDs: string[]) => api.post<Blob>(`${base}/manifest`, { library_ids: libraryIDs }, { responseType: 'blob', timeout: LONG_REQUEST_TIMEOUT }).then(r => r.data),
  preview: (bundle: unknown) => api.post<ProbeExchangePreview>(`${base}/preview`, bundle, { timeout: LONG_REQUEST_TIMEOUT }).then(r => r.data),
  apply: (bundle: unknown, digest: string) => api.post<ProbeExchangePreview>(`${base}/apply`, { bundle, preview_digest: digest, confirmed: true }, { timeout: LONG_REQUEST_TIMEOUT }).then(r => r.data),
}
