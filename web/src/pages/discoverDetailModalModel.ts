import type { DiscoverItem } from '../api/discover'
import { buildSubscribeKeyword } from './discoverPageModel'

export function discoverSubscriptionKeyword(item: DiscoverItem): string {
  return item.subscribe_keyword || buildSubscribeKeyword(item)
}

export function discoverItemMetaText(item: DiscoverItem): string {
  return [
    item.media_type,
    item.year && item.year > 0 ? item.year : '',
    item.rating ? `★ ${item.rating.toFixed(1)}` : '',
  ]
    .filter(Boolean)
    .join(' · ')
}
