import { useCallback, useEffect, useMemo, useState } from 'react'
import { LoaderCircle } from 'lucide-react'

import type { DiscoverItem } from '../api/discover'
import { libraryAPI } from '../api/library'
import type { ResourceImportTask } from '../api/resourceImports'
import type { Library } from '../types'
import { discoverSubscriptionKeyword } from './discoverDetailModalModel'
import { ResourceSearchDrawer } from './ResourceSearchDrawer'
import { mergeResourceImportTasks } from './resourceImportModel'

export function DiscoverResourceAction({ item }: { item: DiscoverItem }) {
  const [libraries, setLibraries] = useState<Library[]>([])
  const [selectedLibraryID, setSelectedLibraryID] = useState('')
  const [tasks, setTasks] = useState<ResourceImportTask[]>([])
  const [taskID, setTaskID] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    libraryAPI
      .list()
      .then((items) => {
        if (cancelled) return
        setLibraries(items.filter((library) => library.enabled))
      })
      .catch(() => {
        if (!cancelled) setError('媒体库加载失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const preferredLibraryID = useMemo(
    () => selectPreferredLibraryID(libraries, item.media_type),
    [item.media_type, libraries],
  )
  const effectiveLibraryID = selectedLibraryID || preferredLibraryID
  const selectedLibrary = libraries.find((library) => library.id === effectiveLibraryID)
  const acceptTask = useCallback((task: ResourceImportTask) => {
    setTasks((current) => mergeResourceImportTasks(current, [task]))
  }, [])

  const selectLibrary = (libraryID: string) => {
    setSelectedLibraryID(libraryID)
    setTasks([])
    setTaskID('')
  }

  return (
    <section className="space-y-3">
      {loading ? (
        <div className="flex h-11 items-center gap-2 text-sm text-sand-500">
          <LoaderCircle size={16} className="animate-spin" />
          正在加载媒体库
        </div>
      ) : error ? (
        <p className="text-sm text-red-500">{error}</p>
      ) : libraries.length === 0 ? (
        <p className="text-sm text-sand-500">当前没有可用媒体库</p>
      ) : (
        <>
          <label className="block text-xs text-sand-500">
            目标媒体库
            <select
              className="input-base mt-1"
              value={effectiveLibraryID}
              onChange={(event) => selectLibrary(event.target.value)}
            >
              {libraries.map((library) => (
                <option key={library.id} value={library.id}>
                  {library.name}
                </option>
              ))}
            </select>
          </label>
          {selectedLibrary && (
            <ResourceSearchDrawer
              key={selectedLibrary.id}
              embedded
              open
              initialQuery={discoverSubscriptionKeyword(item)}
              libraryID={selectedLibrary.id}
              libraryName={selectedLibrary.name}
              libraryRoots={selectedLibrary.roots ?? []}
              tasks={tasks}
              taskID={taskID}
              onTaskIDChange={setTaskID}
              onTaskChanged={acceptTask}
              onClose={() => undefined}
            />
          )}
        </>
      )}
    </section>
  )
}

function selectPreferredLibraryID(libraries: Library[], mediaType?: string): string {
  if (libraries.length === 0) return ''
  const normalizedType = (mediaType || '').toLowerCase()
  const exact = libraries.find((library) => library.type.toLowerCase() === normalizedType)
  if (exact) return exact.id
  if (normalizedType === 'anime') {
    const namedAnime = libraries.find((library) => /动漫|动画|番剧/i.test(library.name))
    if (namedAnime) return namedAnime.id
    const anime = libraries.find((library) => library.type.toLowerCase() === 'tv')
    if (anime) return anime.id
  }
  return libraries[0].id
}
