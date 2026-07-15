import { useCallback, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import toast from 'react-hot-toast'

import { libraryAPI } from '../api/library'
import type { ResourceImportTask } from '../api/resourceImports'
import { useAuthStore } from '../stores/auth'
import type { Library } from '../types'
import { MediaDetailBackdrop } from './MediaDetailArtwork'
import {
  MediaDetailBackButton,
  MediaDetailDialogs,
  MediaDetailLoading,
  MediaDetailMainContent,
  MediaDetailMissing,
} from './MediaDetailPageSections'
import { ResourceSearchDrawer } from './ResourceSearchDrawer'
import { mergeResourceImportTasks } from './resourceImportModel'
import { useMediaDetailPageState } from './useMediaDetailPageState'

export function MediaDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const role = useAuthStore((s) => s.user?.role)
  const detail = useMediaDetailPageState({ id, navigate })
  const [upgradeOpening, setUpgradeOpening] = useState(false)
  const [upgradeOpen, setUpgradeOpen] = useState(false)
  const [upgradeLibrary, setUpgradeLibrary] = useState<Library | null>(null)
  const [upgradeRootID, setUpgradeRootID] = useState('')
  const [upgradeTasks, setUpgradeTasks] = useState<ResourceImportTask[]>([])
  const [upgradeTaskID, setUpgradeTaskID] = useState('')

  const openUpgrade = useCallback(async () => {
    if (!detail.media || upgradeOpening) return
    const currentMedia = detail.media
    setUpgradeOpening(true)
    try {
      const library = await libraryAPI.get(currentMedia.library_id)
      const enabledRoots = (library.roots ?? []).filter((root) => root.enabled)
      const rootID = enabledRoots.some((root) => root.id === currentMedia.library_root_id)
        ? currentMedia.library_root_id ?? ''
        : enabledRoots.length === 1 ? enabledRoots[0].id : ''
      if (!rootID) throw new Error('当前作品缺少明确的媒体库目录，无法升级片源')
      setUpgradeLibrary(library)
      setUpgradeRootID(rootID)
      setUpgradeTaskID('')
      setUpgradeOpen(true)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '加载升级片源入口失败')
    } finally {
      setUpgradeOpening(false)
    }
  }, [detail.media, upgradeOpening])

  const acceptUpgradeTask = useCallback((task: ResourceImportTask) => {
    setUpgradeTasks((current) => mergeResourceImportTasks(current, [task]))
  }, [])

  if (detail.loading) return <MediaDetailLoading />
  if (!detail.media) return <MediaDetailMissing />
  const media = detail.media

  return (
    <div className="relative overflow-hidden rounded-3xl bg-white border border-gray-200/90 shadow-[0_1px_3px_rgba(0,0,0,0.01),0_1px_2px_rgba(0,0,0,0.015)]">
      <MediaDetailBackdrop media={media} />

      <MediaDetailBackButton onBack={detail.goBack} />

      <MediaDetailMainContent
        media={media}
        isAdmin={role === 'admin'}
        favourite={detail.favourite}
        scrapeEpisodeArtwork={detail.scrapeEpisodeArtwork}
        onToggleFavourite={detail.toggleFavourite}
        onUpgrade={() => void openUpgrade()}
        upgradeOpening={upgradeOpening}
        onScrapeEpisodeArtworkChange={detail.setScrapeEpisodeArtwork}
        onSmartScrape={detail.rescrape}
        onManualScrape={() => detail.setManualScrapeOpen(true)}
        onMetadataEdit={() => detail.setMetadataEditOpen(true)}
        onOrganize={() => detail.setOrganizeOpen(true)}
        onProbe={detail.reprobe}
        onExportNFO={detail.exportNFO}
        onSoftDelete={detail.softDelete}
      />
      <MediaDetailDialogs
        media={media}
        manualScrapeOpen={detail.manualScrapeOpen}
        metadataEditOpen={detail.metadataEditOpen}
        organizeOpen={detail.organizeOpen}
        scrapeEpisodeArtwork={detail.scrapeEpisodeArtwork}
        onManualScrapeClose={() => detail.setManualScrapeOpen(false)}
        onMetadataEditClose={() => detail.setMetadataEditOpen(false)}
        onOrganizeClose={() => detail.setOrganizeOpen(false)}
        onManualScrapeApplied={detail.refresh}
        onMetadataSaved={detail.handleMetadataSaved}
        onOrganized={detail.refresh}
      />
      {upgradeLibrary && (
        <ResourceSearchDrawer
          open={upgradeOpen}
          initialQuery={media.display_title?.trim() || media.title}
          upgradeMediaID={media.id}
          fixedRootID={upgradeRootID}
          libraryID={upgradeLibrary.id}
          libraryName={upgradeLibrary.name}
          libraryRoots={upgradeLibrary.roots ?? []}
          tasks={upgradeTasks}
          taskID={upgradeTaskID}
          onTaskIDChange={setUpgradeTaskID}
          onTaskChanged={acceptUpgradeTask}
          onClose={() => setUpgradeOpen(false)}
        />
      )}
    </div>
  )
}
