import { api } from './client'
import type { Media } from '../types'

// TMDb-derived "Match" rows used by trending/popular rails. We re-use the
// Media interface — only TMDb id / poster / overview are populated.
export interface DiscoverItem extends Partial<Media> {
  source?: string
  media_type?: string
  tmdb_id?: number
  douban_id?: string
  bangumi_id?: number
  title: string
  poster_url?: string
  backdrop_url?: string
  overview?: string
  year?: number
  rating?: number
  subscribe_keyword?: string
  subscribe_aliases?: string[]
  total_episodes?: number
  downloaded_episodes?: number
  local_media_count?: number
  missing_episodes?: number[]
  in_library?: boolean
  media_id?: string
  library_id?: string
	provider_url?: string
	provider_id?: string
	followed?: boolean
	people?: DiscoverPerson[]
}

export interface DiscoverPerson {
	name: string
	image_url?: string
	profile_url?: string
	source?: string
	source_id?: string
}

export interface DiscoverSection {
  key: string
  label: string
  provider?: string
	group?: string
}

export interface AdultPerformerFollow {
	id: string
	name: string
	source: string
	source_id: string
	image_url?: string
	profile_url?: string
}

export interface DiscoverFeedMeta {
  page: number
  has_next: boolean
  duration_ms?: number
  error?: string
  warning?: string
  stale?: boolean
  disabled?: boolean
}

export interface DiscoverFeedResult {
  items: Record<string, DiscoverItem[]>
  meta: Record<string, DiscoverFeedMeta>
}

// 后端在 TMDb 不可达 / API key 缺失时统一返回 { items: [], error: "..." }
// 200 状态码——前端必须能区分这两种情况，不能简单用 items.length === 0
// 推断"未配置 API key"。
export interface DiscoverResp {
  items: DiscoverItem[]
  error?: string
}

export const discoverAPI = {
  trending: () =>
    api.get<DiscoverResp>('/discover/trending').then((r) => ({
      items: r.data.items ?? [],
      error: r.data.error,
    })),
  popular: () =>
    api.get<DiscoverResp>('/discover/popular').then((r) => ({
      items: r.data.items ?? [],
      error: r.data.error,
    })),
  sections: () =>
    api.get<{ sections: DiscoverSection[] }>('/discover/sections').then((r) => r.data.sections),
  feed: (sectionKeys: string[], page = 1): Promise<DiscoverFeedResult> =>
    api
      .get<Record<string, DiscoverItem[] | DiscoverFeedMeta | Record<string, DiscoverFeedMeta> | null>>('/discover/feed', {
        params: { sections: sectionKeys.join(','), page },
      })
      .then((r) => {
        const raw = r.data
        const meta = ((raw._meta as Record<string, DiscoverFeedMeta> | undefined) ?? {})
        const items: Record<string, DiscoverItem[]> = {}
        for (const key of sectionKeys) {
          const row = raw[key]
          items[key] = Array.isArray(row) ? row : []
        }
        return { items, meta }
      }),
	adultFollows: () =>
		api.get<{ items: AdultPerformerFollow[] }>('/discover/adult/follows').then((r) => r.data.items ?? []),
	followAdultPerformer: (input: {
		name: string
		source: string
		source_id: string
		image_url?: string
	}) => api.post<AdultPerformerFollow>('/discover/adult/follows', input).then((r) => r.data),
	unfollowAdultPerformer: (id: string) => api.delete(`/discover/adult/follows/${id}`),
	adultPerformerWorks: (source: string, sourceID: string, page = 1) =>
		api
			.get<{ items: DiscoverItem[]; page: number; has_next: boolean }>(
				`/discover/adult/performers/${encodeURIComponent(source)}/${encodeURIComponent(sourceID)}/works`,
				{ params: { page } },
			)
			.then((r) => r.data),
}
