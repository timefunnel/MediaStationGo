import { useEffect, useState } from 'react'
import { X } from 'lucide-react'

import { discoverAPI } from '../api/discover'
import type { DiscoverItem } from '../api/discover'
import { imageURL } from '../api/client'
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
  const [artworkPreviewOpen, setArtworkPreviewOpen] = useState(false)
  const source = discoverItemSource(resolvedItem)

  useEffect(() => {
    setResolvedItem(item)
    setDetailError('')
    setArtworkPreviewOpen(false)
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

  useEffect(() => {
    if (!artworkPreviewOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setArtworkPreviewOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [artworkPreviewOpen])

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
              <DiscoverArtworkPanel
                item={resolvedItem}
                deferred={detailLoading}
                onPreview={() => setArtworkPreviewOpen(true)}
              />
              <div className="space-y-5">{metadata}</div>
            </div>
            <div className="mt-5 border-t border-gray-200 pt-5">
              <DiscoverResourceAction item={resolvedItem} />
            </div>
          </>
        ) : (
          <div className="grid gap-5 lg:grid-cols-[260px_1fr]">
            <DiscoverArtworkPanel item={resolvedItem} onPreview={() => setArtworkPreviewOpen(true)} />
            <div className="space-y-5">
              {metadata}
              <DiscoverResourceAction item={resolvedItem} />
            </div>
          </div>
        )}
      </div>
      {artworkPreviewOpen && resolvedItem.poster_url && (
        <div
          className="fixed inset-0 z-[90] flex items-center justify-center bg-black/90 p-4 sm:p-8"
          role="dialog"
          aria-modal="true"
          aria-label={`${resolvedItem.title} 大图预览`}
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) setArtworkPreviewOpen(false)
          }}
        >
          <img
            src={imageURL(resolvedItem.poster_url)}
            alt={resolvedItem.title}
            className="max-h-full max-w-full object-contain shadow-2xl"
          />
          <button
            type="button"
            className="absolute right-4 top-4 rounded-full border border-white/30 bg-black/55 p-2.5 text-white transition hover:bg-black/80 sm:right-6 sm:top-6"
            aria-label="关闭大图预览"
            title="关闭"
            onClick={() => setArtworkPreviewOpen(false)}
          >
            <X size={22} />
          </button>
        </div>
      )}
    </div>
  )
}
