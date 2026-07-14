import type { ReactNode } from 'react'
import { Film } from 'lucide-react'

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
  return (
    <>
      {!isSeries && items.length > 0 && (
        <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7 2xl:grid-cols-8">
          {items.map((media) => (
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
          {seriesCards.map((series) => (
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
    </>
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
