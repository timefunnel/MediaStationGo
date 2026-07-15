import { useEffect, useMemo, useState } from 'react'
import { useLocation, useParams, useSearchParams } from 'react-router-dom'
import { motion } from 'framer-motion'

import type { Media } from '../types'
import { useAuthStore } from '../stores/auth'
import type { SeriesCard } from '../utils/groupSeries'
import { LibraryPageDialogs } from './LibraryPageDialogs'
import { LibraryPageHeader } from './LibraryPageHeader'
import { LibraryMediaSections } from './LibraryMediaSections'
import { LibrarySeriesDetailSection } from './LibrarySeriesDetailSection'
import { useLibraryData } from './useLibraryData'
import { useLibraryScanStatus } from './useLibraryScanStatus'
import { useLibrarySeriesSelection } from './useLibrarySeriesSelection'
import { useLibraryAdminActions } from './useLibraryAdminActions'
import { useLibraryResourceImports } from './useLibraryResourceImports'
import { LibraryResourceImportStatus } from './LibraryResourceImportStatus'
import { ResourceSearchDrawer } from './ResourceSearchDrawer'
import { LibraryActorFilter } from './LibraryActorFilter'
import { buildActorFacets, mediaHasActor } from './libraryActorFilterModel'

export function LibraryPage() {
  const { id = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const role = useAuthStore((s) => s.user?.role)
  const userID = useAuthStore((s) => s.user?.id ?? '')

  const [manualSeriesScrapeOpen, setManualSeriesScrapeOpen] = useState(false)
  const [seriesMetadataEditOpen, setSeriesMetadataEditOpen] = useState(false)
  const [manualMovie, setManualMovie] = useState<Media | null>(null)
  const [resourceDrawerOpen, setResourceDrawerOpen] = useState(false)
  const [resourceInitialQuery, setResourceInitialQuery] = useState('')
  const [resourceTaskID, setResourceTaskID] = useState('')

  // 剧集模式：选中某个剧集后展开详情
  const [selectedSeries, setSelectedSeries] = useState<SeriesCard | null>(null)
  const [selectedSeason, setSelectedSeason] = useState<number | null>(null)

  const {
    library,
    items,
    seriesEpisodeItems,
    total,
    loading,
    loadingSeriesEpisodes,
    isSeriesLibrary,
    isSeries,
    seriesCards,
    loadingAllText,
    reloadCurrentLibrary,
  } = useLibraryData(id, selectedSeries)

  const {
    scanning,
    scanProgress,
    handleScan,
  } = useLibraryScanStatus({
    libraryID: id,
    isAdmin: role === 'admin',
    onLibraryChanged: reloadCurrentLibrary,
  })

  const {
    selectedEpisodes,
    visibleEpisodes,
    selectedSeriesEpisodes,
    selectedSeriesMediaIDs,
    handleSeriesClick,
    clearSelectedSeries,
  } = useLibrarySeriesSelection({
    items,
    seriesEpisodeItems,
    isSeriesLibrary,
    isSeries,
    loading,
    seriesCards,
    searchParams,
    setSearchParams,
    selectedSeries,
    setSelectedSeries,
    selectedSeason,
    setSelectedSeason,
    onClearSeriesState: () => setSeriesMetadataEditOpen(false),
  })

  const {
    scraping,
    scrapeEpisodeArtwork,
    repairing,
    seriesToolBusy,
    setScrapeEpisodeArtwork,
    handleScrape,
    handleRepairRescrape,
    handleSeriesSmartScrape,
    handleSeriesProbe,
    handleSeriesNFO,
    handleSeriesOrganize,
    handleSeriesSoftDelete,
    movieActions,
  } = useLibraryAdminActions({
    libraryID: id,
    role,
    library,
    selectedSeries,
    selectedSeriesEpisodes,
    reloadCurrentLibrary,
    clearSelectedSeries,
    setManualMovie,
  })

  const resourceImports = useLibraryResourceImports(id, userID, reloadCurrentLibrary)
  const actorFacets = useMemo(() => buildActorFacets(items), [items])
  const selectedActor = searchParams.get('actor')?.trim() ?? ''
  const requestedResourceQuery = searchParams.get('resource_query')?.trim() ?? ''
  const filteredItems = useMemo(
    () => selectedActor ? items.filter((media) => mediaHasActor(media, selectedActor)) : items,
    [items, selectedActor],
  )

  useEffect(() => {
    if (loading || !selectedActor || actorFacets.some((actor) => actor.name === selectedActor)) return
    const next = new URLSearchParams(searchParams)
    next.delete('actor')
    setSearchParams(next, { replace: true })
  }, [actorFacets, loading, searchParams, selectedActor, setSearchParams])

  useEffect(() => {
    if (loading || !library || !requestedResourceQuery) return
    setResourceInitialQuery(requestedResourceQuery)
    setResourceTaskID('')
    setResourceDrawerOpen(true)
    const next = new URLSearchParams(searchParams)
    next.delete('resource_query')
    setSearchParams(next, { replace: true })
  }, [library, loading, requestedResourceQuery, searchParams, setSearchParams])

  const selectActor = (actor: string) => {
    const next = new URLSearchParams(searchParams)
    if (actor) next.set('actor', actor)
    else next.delete('actor')
    setSearchParams(next)
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center py-32">
        <motion.div animate={{ opacity: [0.4, 1, 0.4] }} transition={{ repeat: Infinity, duration: 2 }} className="flex items-center gap-3">
          <div className="h-2 w-2 rounded-full bg-brand-500" />
          <span className="text-sm text-sand-500">加载中…</span>
        </motion.div>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <LibraryPageHeader
        library={library}
        itemCount={isSeries ? seriesCards.length : selectedActor ? filteredItems.length : total}
        loadingAllText={loadingAllText}
        scanProgress={scanProgress}
        isAdmin={role === 'admin'}
        scrapeEpisodeArtwork={scrapeEpisodeArtwork}
        scanning={scanning}
        scraping={scraping}
        repairing={repairing}
        onScrapeEpisodeArtworkChange={setScrapeEpisodeArtwork}
        onScan={handleScan}
        onScrape={handleScrape}
        onRepairRescrape={handleRepairRescrape}
        onResourceSearch={() => {
          setResourceInitialQuery('')
          setResourceTaskID('')
          setResourceDrawerOpen(true)
        }}
      />

      <LibraryResourceImportStatus
        activeTasks={resourceImports.activeTasks}
        latestCompletedTask={resourceImports.latestCompletedTask}
        loading={resourceImports.loading}
        error={resourceImports.error}
        onOpenTask={(task) => {
          setResourceTaskID(task.id)
          setResourceDrawerOpen(true)
        }}
        onDismissCompleted={resourceImports.dismissCompletedTask}
        onRetryLoad={() => void resourceImports.refresh()}
      />

      {!isSeries && (
        <LibraryActorFilter actors={actorFacets} selected={selectedActor} onChange={selectActor} />
      )}

      <LibraryMediaSections
        isSeries={isSeries}
        items={filteredItems}
        seriesCards={seriesCards}
        selectedSeries={selectedSeries}
        loading={loading}
        movieActions={movieActions}
        onSeriesClick={handleSeriesClick}
        highlightedMediaID={resourceImports.highlightedMediaID}
      />

      <LibrarySeriesDetailSection
        selectedSeries={selectedSeries}
        selectedEpisodes={selectedEpisodes}
        selectedSeason={selectedSeason}
        visibleEpisodes={visibleEpisodes}
        allEpisodes={selectedSeriesEpisodes}
        loadingEpisodes={loadingSeriesEpisodes}
        playbackFrom={`${location.pathname}${location.search}`}
        isAdmin={role === 'admin'}
        seriesToolBusy={seriesToolBusy}
        onBack={clearSelectedSeries}
        onSmartScrape={handleSeriesSmartScrape}
        onManualScrape={() => setManualSeriesScrapeOpen(true)}
        onMetadataEdit={() => setSeriesMetadataEditOpen(true)}
        onProbe={handleSeriesProbe}
        onNFO={handleSeriesNFO}
        onOrganize={handleSeriesOrganize}
        onSoftDelete={handleSeriesSoftDelete}
        onSeasonChange={setSelectedSeason}
      />

      <LibraryPageDialogs
        manualSeriesScrapeOpen={manualSeriesScrapeOpen}
        seriesMetadataEditOpen={seriesMetadataEditOpen}
        manualMovie={manualMovie}
        selectedSeries={selectedSeries}
        selectedSeriesMediaIDs={selectedSeriesMediaIDs}
        libraryType={library?.type}
        scrapeEpisodeArtwork={scrapeEpisodeArtwork}
        onCloseManualSeriesScrape={() => setManualSeriesScrapeOpen(false)}
        onCloseSeriesMetadataEdit={() => setSeriesMetadataEditOpen(false)}
        onCloseManualMovie={() => setManualMovie(null)}
        onApplied={reloadCurrentLibrary}
      />

      <ResourceSearchDrawer
        open={resourceDrawerOpen}
        initialQuery={resourceInitialQuery}
        libraryID={id}
        libraryName={library?.name ?? '媒体库'}
        libraryRoots={library?.roots ?? []}
        tasks={resourceImports.tasks}
        taskID={resourceTaskID}
        onTaskIDChange={setResourceTaskID}
        onTaskChanged={resourceImports.acceptTask}
        onClose={() => {
          setResourceDrawerOpen(false)
          setResourceInitialQuery('')
        }}
      />
    </div>
  )
}
