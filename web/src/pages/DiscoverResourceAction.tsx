import { useEffect, useMemo, useState } from 'react'
import { Globe, LoaderCircle } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import type { DiscoverItem } from '../api/discover'
import { libraryAPI } from '../api/library'
import type { Library } from '../types'
import { discoverSubscriptionKeyword } from './discoverDetailModalModel'

export function DiscoverResourceAction({
  item,
  onNavigate,
}: {
  item: DiscoverItem
  onNavigate?: () => void
}) {
  const navigate = useNavigate()
  const [libraries, setLibraries] = useState<Library[]>([])
  const [selectedLibraryID, setSelectedLibraryID] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    libraryAPI
      .list()
      .then((items) => {
        if (cancelled) return
        setLibraries(items.filter((library) => library.enabled))
      })
      .catch(() => {
        if (!cancelled) setError('媒体库加载失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const preferredLibraryID = useMemo(
    () => selectPreferredLibraryID(libraries, item.media_type),
    [item.media_type, libraries],
  )
  const effectiveLibraryID = selectedLibraryID || preferredLibraryID

  const openResourceSearch = () => {
    if (!effectiveLibraryID) return
    const query = discoverSubscriptionKeyword(item)
    onNavigate?.()
    navigate(`/library/${effectiveLibraryID}?resource_query=${encodeURIComponent(query)}`)
  }

  return (
    <section className="rounded-lg border border-primary-400/30 bg-primary-400/5 p-4">
      <h3 className="mb-3 flex items-center gap-2 font-semibold text-ink-600">
        <Globe size={17} />
        查找资源
      </h3>
      {loading ? (
        <div className="flex h-11 items-center gap-2 text-sm text-sand-500">
          <LoaderCircle size={16} className="animate-spin" />
          正在加载媒体库
        </div>
      ) : error ? (
        <p className="text-sm text-red-500">{error}</p>
      ) : libraries.length === 0 ? (
        <p className="text-sm text-sand-500">当前没有可用媒体库</p>
      ) : (
        <div className="flex flex-col gap-3 sm:flex-row">
          <label className="min-w-0 flex-1 text-xs text-sand-500">
            目标媒体库
            <select
              className="input-base mt-1"
              value={effectiveLibraryID}
              onChange={(event) => setSelectedLibraryID(event.target.value)}
            >
              {libraries.map((library) => (
                <option key={library.id} value={library.id}>
                  {library.name}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            className="btn-primary h-11 shrink-0 self-end px-4"
            onClick={openResourceSearch}
          >
            <Globe size={17} />
            查找资源
          </button>
        </div>
      )}
    </section>
  )
}

function selectPreferredLibraryID(libraries: Library[], mediaType?: string): string {
  if (libraries.length === 0) return ''
  const normalizedType = (mediaType || '').toLowerCase()
  const exact = libraries.find((library) => library.type.toLowerCase() === normalizedType)
  if (exact) return exact.id
  if (normalizedType === 'anime') {
    const namedAnime = libraries.find((library) => /动漫|动画|番剧/i.test(library.name))
    if (namedAnime) return namedAnime.id
    const anime = libraries.find((library) => library.type.toLowerCase() === 'tv')
    if (anime) return anime.id
  }
  return libraries[0].id
}
