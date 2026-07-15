import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { ChevronLeft, ChevronRight, Film } from 'lucide-react'

import { MediaCard } from '../components/MediaCard'
import type { Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'

type LibraryMediaSectionsProps = {
  isSeries: boolean
  items: Media[]
  seriesCards: SeriesCard[]
  selectedSeries: SeriesCard | null
  loading: boolean
  movieActions: (media: Media) => ReactNode
  onSeriesClick: (series: SeriesCard) => void
  highlightedMediaID?: string
}

const LIBRARY_CARD_PAGE_SIZE = 48

export function LibraryMediaSections({
  isSeries,
  items,
  seriesCards,
  selectedSeries,
  loading,
  movieActions,
  onSeriesClick,
  highlightedMediaID,
}: LibraryMediaSectionsProps) {
  const [page, setPage] = useState(1)
  const entryCount = isSeries ? seriesCards.length : items.length
  const totalPages = Math.max(1, Math.ceil(entryCount / LIBRARY_CARD_PAGE_SIZE))
  const visibleItems = useMemo(
    () => items.slice((page - 1) * LIBRARY_CARD_PAGE_SIZE, page * LIBRARY_CARD_PAGE_SIZE),
    [items, page],
  )
  const visibleSeries = useMemo(
    () => seriesCards.slice((page - 1) * LIBRARY_CARD_PAGE_SIZE, page * LIBRARY_CARD_PAGE_SIZE),
    [page, seriesCards],
  )

  useEffect(() => {
    const highlightedIndex = highlightedMediaID
      ? (isSeries
          ? seriesCards.findIndex((series) => series.rep.id === highlightedMediaID || series.linkMedia.id === highlightedMediaID)
          : items.findIndex((media) => media.id === highlightedMediaID))
      : -1
    if (highlightedIndex >= 0) {
      setPage(Math.floor(highlightedIndex / LIBRARY_CARD_PAGE_SIZE) + 1)
      return
    }
    setPage((current) => Math.min(Math.max(1, current), totalPages))
  }, [highlightedMediaID, isSeries, items, seriesCards, totalPages])

  return (
    <>
      {!isSeries && items.length > 0 && (
        <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7 2xl:grid-cols-8">
          {visibleItems.map((media) => (
            <div
              key={media.id}
              className={highlightedMediaID === media.id ? 'rounded-lg ring-4 ring-emerald-400/70 ring-offset-2' : ''}
            >
              <MediaCard media={media} actions={movieActions(media)} />
            </div>
          ))}
        </div>
      )}

      {!isSeries && items.length === 0 && (
        <LibraryEmptyState message="该媒体库暂无内容，触发一次扫描后再来看看" />
      )}

      {isSeries && seriesCards.length > 0 && !selectedSeries && (
        <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7 2xl:grid-cols-8">
          {visibleSeries.map((series) => (
            <div
              key={series.key}
              className={
                highlightedMediaID === series.rep.id || highlightedMediaID === series.linkMedia.id
                  ? 'rounded-lg ring-4 ring-emerald-400/70 ring-offset-2'
                  : ''
              }
            >
              <MediaCard
                media={series.rep}
                count={series.count}
                onClick={() => onSeriesClick(series)}
              />
            </div>
          ))}
        </div>
      )}

      {isSeries && seriesCards.length === 0 && !loading && (
        <LibraryEmptyState message="该库尚未发现任何剧集，触发一次扫描后再来看看" />
      )}

      {!selectedSeries && entryCount > 0 && totalPages > 1 && (
        <LibraryPagination page={page} totalPages={totalPages} onPageChange={setPage} />
      )}
    </>
  )
}

function LibraryPagination({
  page,
  totalPages,
  onPageChange,
}: {
  page: number
  totalPages: number
  onPageChange: (page: number) => void
}) {
  return (
    <div className="flex items-center justify-center gap-3 pt-2">
      <button
        type="button"
        className="btn-outline h-10 w-10 justify-center p-0"
        disabled={page <= 1}
        onClick={() => onPageChange(Math.max(1, page - 1))}
        aria-label="上一页"
        title="上一页"
      >
        <ChevronLeft size={17} />
      </button>
      <label className="flex h-10 items-center gap-2 text-sm text-ink-50">
        <span>第</span>
        <select
          className="input-base h-9 min-w-20 py-1 text-center"
          value={page}
          onChange={(event) => onPageChange(Number(event.target.value))}
          aria-label="跳转页码"
        >
          {Array.from({ length: totalPages }, (_, index) => index + 1).map((value) => (
            <option key={value} value={value}>{value}</option>
          ))}
        </select>
        <span>/ {totalPages} 页</span>
      </label>
      <button
        type="button"
        className="btn-outline h-10 w-10 justify-center p-0"
        disabled={page >= totalPages}
        onClick={() => onPageChange(Math.min(totalPages, page + 1))}
        aria-label="下一页"
        title="下一页"
      >
        <ChevronRight size={17} />
      </button>
    </div>
  )
}

function LibraryEmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <Film className="mb-4 h-12 w-12 text-gray-500" />
      <p className="text-ink-50">{message}</p>
    </div>
  )
}
