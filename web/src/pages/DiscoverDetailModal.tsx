import { useEffect, useState } from 'react'

import { discoverAPI } from '../api/discover'
import type { DiscoverItem } from '../api/discover'
import { discoverItemSource } from './discoverPageModel'
import {
  DiscoverArtworkPanel,
  DiscoverMetadataPanel,
  DiscoverModalHeader,
  DiscoverOverviewPanel,
} from './DiscoverDetailModalSections'
import { mergeDiscoverDetail, supportsAdultMovieDetail } from './discoverDetailModalModel'
import { DiscoverResourceAction } from './DiscoverResourceAction'

export function DiscoverDetailModal({
  item,
  onClose,
  onSelectPerformer,
}: {
  item: DiscoverItem
  onClose: () => void
  onSelectPerformer?: (item: DiscoverItem) => void
}) {
  const [resolvedItem, setResolvedItem] = useState(item)
  const [detailLoading, setDetailLoading] = useState(() => supportsAdultMovieDetail(item))
  const [detailError, setDetailError] = useState('')
  const source = discoverItemSource(resolvedItem)

  useEffect(() => {
    setResolvedItem(item)
    setDetailError('')
    if (!supportsAdultMovieDetail(item)) {
      setDetailLoading(false)
      return
    }
    let cancelled = false
    setDetailLoading(true)
    discoverAPI
      .adultMovieDetail(item.source!, item.provider_id!, item.original_name!)
      .then((detail) => {
        if (!cancelled) setResolvedItem(mergeDiscoverDetail(item, detail))
      })
      .catch((error) => {
        if (cancelled) return
        const message = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
        setDetailError(message || '作品详细资料加载失败')
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [item])

  const metadata = (
    <>
      <DiscoverMetadataPanel
        item={resolvedItem}
        loading={detailLoading}
        error={detailError}
        onSelectPerformer={onSelectPerformer}
      />
      <DiscoverOverviewPanel overview={resolvedItem.overview} />
    </>
  )

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-sm">
      <div className="max-h-[92vh] w-full max-w-5xl overflow-y-auto rounded-3xl border border-white/60 bg-white p-5 shadow-2xl">
        <DiscoverModalHeader item={resolvedItem} source={source} onClose={onClose} />
        {resolvedItem.media_type === 'adult' ? (
          <>
            <div className="grid items-start gap-5 lg:grid-cols-[minmax(340px,42%)_1fr]">
              <DiscoverArtworkPanel item={resolvedItem} deferred={detailLoading} />
              <div className="space-y-5">{metadata}</div>
            </div>
            <div className="mt-5 border-t border-gray-200 pt-5">
              <DiscoverResourceAction item={resolvedItem} />
            </div>
          </>
        ) : (
          <div className="grid gap-5 lg:grid-cols-[260px_1fr]">
            <DiscoverArtworkPanel item={resolvedItem} />
            <div className="space-y-5">
              {metadata}
              <DiscoverResourceAction item={resolvedItem} />
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
