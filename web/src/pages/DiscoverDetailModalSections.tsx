import { X } from 'lucide-react'

import type { DiscoverItem } from '../api/discover'
import { imageURL } from '../api/client'
import { discoverItemMetaText } from './discoverDetailModalModel'

export function DiscoverModalHeader({ item, source, onClose }: { item: DiscoverItem; source: string; onClose: () => void }) {
  return (
    <div className="mb-4 flex items-start justify-between gap-3">
      <div>
        <p className="text-xs font-semibold uppercase tracking-widest text-brand-500">{source}</p>
        <h2 className="font-display text-2xl font-bold text-ink-600">{item.title}</h2>
        <p className="mt-1 text-sm text-sand-500">{discoverItemMetaText(item)}</p>
      </div>
      <button className="rounded-full border border-gray-200 p-2 text-ink-50 hover:bg-gray-50" onClick={onClose}>
        <X size={18} />
      </button>
    </div>
  )
}

export function DiscoverArtworkPanel({ item }: { item: DiscoverItem }) {
  return (
    <div className="space-y-3">
      <div className="overflow-hidden rounded-2xl bg-gray-100">
        {item.poster_url ? (
          <img src={imageURL(item.poster_url)} alt={item.title} className="aspect-[2/3] w-full object-cover" />
        ) : (
          <div className="flex aspect-[2/3] items-center justify-center text-sand-500">无海报</div>
        )}
      </div>
      {item.backdrop_url && (
        <img src={imageURL(item.backdrop_url)} alt="" className="h-24 w-full rounded-2xl object-cover" />
      )}
    </div>
  )
}

export function DiscoverOverviewPanel({ overview }: { overview?: string }) {
  return (
    <section className="rounded-2xl border border-gray-200 bg-gray-50 p-4">
      <h3 className="mb-2 font-semibold text-ink-600">简介</h3>
      <p className="text-sm leading-6 text-ink-100">{overview || '当前数据源没有返回简介。'}</p>
    </section>
  )
}
