import type { DiscoverItem } from '../api/discover'
import { discoverItemSource } from './discoverPageModel'
import {
  DiscoverArtworkPanel,
  DiscoverModalHeader,
  DiscoverOverviewPanel,
} from './DiscoverDetailModalSections'
import { DiscoverResourceAction } from './DiscoverResourceAction'

export function DiscoverDetailModal({ item, onClose }: { item: DiscoverItem; onClose: () => void }) {
  const source = discoverItemSource(item)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4 backdrop-blur-sm">
      <div className="max-h-[92vh] w-full max-w-5xl overflow-y-auto rounded-3xl border border-white/60 bg-white p-5 shadow-2xl">
        <DiscoverModalHeader item={item} source={source} onClose={onClose} />
        <div className="grid gap-5 lg:grid-cols-[260px_1fr]">
          <DiscoverArtworkPanel item={item} />
          <div className="space-y-5">
            <DiscoverOverviewPanel overview={item.overview} />
            <DiscoverResourceAction item={item} />
          </div>
        </div>
      </div>
    </div>
  )
}
