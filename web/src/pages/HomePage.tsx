import { useEffect, useMemo, useState } from 'react'

import { libraryAPI, mediaAPI } from '../api/library'
import { playbackAPI, type HistoryItem } from '../api/playback'
import { usePermission } from '../hooks/usePermission'
import type { Library, Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'
import { hasMediaArtwork, mediaBackdropArtworkURL, mediaPrimaryArtworkURL } from '../utils/mediaArtwork'
import {
  ContinueWatchingSection,
  HomeEmptyState,
  HomeFeaturedSection,
  HomeLoadError,
  HomeLoadingState,
  RecentMediaSection,
} from './HomePageSections'

const asArray = <T,>(value: unknown): T[] => (Array.isArray(value) ? value as T[] : [])

export function HomePage() {
  const [libraries, setLibraries] = useState<Library[]>([])
  const [recentCards, setRecentCards] = useState<SeriesCard[]>([])
  const [history, setHistory] = useState<HistoryItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [loadVersion, setLoadVersion] = useState(0)
  const canPlayMedia = usePermission('can_play_media')
  const canViewHistory = usePermission('can_view_history')

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      setError('')
      try {
        const [libs, recentItems, hist] = await Promise.all([
          canPlayMedia ? libraryAPI.list().then((rows) => asArray<Library>(rows)) : Promise.resolve([] as Library[]),
          mediaAPI.recent(24).then((rows) => asArray<SeriesCard>(rows)),
          canViewHistory ? playbackAPI.recentHistory().then((rows) => asArray<HistoryItem>(rows)) : Promise.resolve([] as HistoryItem[]),
        ])
        if (cancelled) return
        setLibraries(libs)
        setRecentCards(recentItems)
        setHistory(hist.filter((h) => h && !h.completed && !!h.media))
      } catch (err) {
        if (cancelled) return
        setLibraries([])
        setRecentCards([])
        setHistory([])
        setError((err as { response?: { data?: { error?: string } } })?.response?.data?.error || '首页内容加载失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [canPlayMedia, canViewHistory, loadVersion])

  const featuredItem = useMemo(() => {
    const candidates = [
      ...(history.map((h) => h.media).filter(Boolean) as Media[]),
      ...recentCards.map((card) => card.rep),
    ]
    return candidates.find(hasMediaArtwork) ?? candidates[0] ?? null
  }, [history, recentCards])
  const featuredVisual = mediaBackdropArtworkURL(featuredItem)
  const featuredPoster = mediaPrimaryArtworkURL(featuredItem)
  const featuredMark = (featuredItem?.title || 'MS').trim().slice(0, 4).toUpperCase()
  const empty = !loading && libraries.length === 0 && recentCards.length === 0 && history.length === 0

  if (loading) {
    return <HomeLoadingState />
  }

  if (error) {
    return <HomeLoadError message={error} onRetry={() => setLoadVersion((version) => version + 1)} />
  }

  if (empty) {
    return <HomeEmptyState />
  }

  return (
    <div className="space-y-12">
      {featuredItem && (
        <HomeFeaturedSection
          featuredItem={featuredItem}
          featuredVisual={featuredVisual}
          featuredPoster={featuredPoster}
          featuredMark={featuredMark}
        />
      )}

      {history.length > 0 && <ContinueWatchingSection history={history} />}
      {recentCards.length > 0 && <RecentMediaSection recentCards={recentCards} />}
    </div>
  )
}
