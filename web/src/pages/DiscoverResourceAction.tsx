import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, BellPlus, Check, LoaderCircle } from 'lucide-react'
import toast from 'react-hot-toast'

import type { DiscoverItem } from '../api/discover'
import { libraryAPI } from '../api/library'
import type { ResourceImportTask } from '../api/resourceImports'
import { buildResourceImportFeedURL, buildSubscriptionAliases, subscriptionsAPI } from '../api/subscriptions'
import { useAuthStore } from '../stores/auth'
import type { Library } from '../types'
import { discoverResourceSearchKeyword } from './discoverDetailModalModel'
import { ResourceSearchDrawer } from './ResourceSearchDrawer'
import { mergeResourceImportTasks } from './resourceImportModel'

export function DiscoverResourceAction({ item }: { item: DiscoverItem }) {
  const [libraries, setLibraries] = useState<Library[]>([])
  const [selectedLibraryID, setSelectedLibraryID] = useState('')
  const [tasks, setTasks] = useState<ResourceImportTask[]>([])
  const [taskID, setTaskID] = useState('')
  const [subscriptionRootID, setSubscriptionRootID] = useState('')
  const [subscribing, setSubscribing] = useState(false)
  const [subscribed, setSubscribed] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const isAdmin = useAuthStore((state) => state.user?.role === 'admin')

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
  const enabledRoots = (selectedLibrary?.roots ?? []).filter((root) => root.enabled)
  const effectiveSubscriptionRootID = subscriptionRootID || (enabledRoots.length === 1 ? enabledRoots[0].id : '')
  const canSubscribe = isAdmin && ['tv', 'anime', 'variety'].includes((item.media_type || '').toLowerCase())
  const acceptTask = useCallback((task: ResourceImportTask) => {
    setTasks((current) => mergeResourceImportTasks(current, [task]))
  }, [])

  const selectLibrary = (libraryID: string) => {
    setSelectedLibraryID(libraryID)
    const roots = (libraries.find((library) => library.id === libraryID)?.roots ?? []).filter((root) => root.enabled)
    setSubscriptionRootID(roots.length === 1 ? roots[0].id : '')
    setSubscribed(false)
    setTasks([])
    setTaskID('')
  }

  const createSubscription = async () => {
    if (!selectedLibrary || !effectiveSubscriptionRootID || subscribing) return
    setSubscribing(true)
    try {
      const aliases = buildSubscriptionAliases(item)
      await subscriptionsAPI.create({
        name: item.title,
        feed_url: buildResourceImportFeedURL(aliases),
        delivery_mode: 'resource_import',
        library_id: selectedLibrary.id,
        library_root_id: effectiveSubscriptionRootID,
        resource_source: 'pansou',
        max_imports_per_run: 2,
        season_number: 1,
        filter: item.subscribe_keyword || item.title,
        original_name: item.original_name,
        year: item.year,
        media_type: item.media_type || selectedLibrary.type,
        poster_url: item.poster_url,
        backdrop_url: item.backdrop_url,
        overview: item.overview,
        total_episodes: item.total_episodes,
        resolution: 'best',
        enabled: true,
      })
      setSubscribed(true)
      toast.success('已创建网盘追更订阅')
    } catch (requestError) {
      const message = (requestError as { response?: { data?: { error?: string } } })?.response?.data?.error
      toast.error(message || '创建订阅失败')
    } finally {
      setSubscribing(false)
    }
  }

  return (
    <section className="space-y-3">
      {item.in_library && item.media_id && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-emerald-200 bg-emerald-50 p-3">
          <div>
            <p className="text-sm font-semibold text-emerald-800">该作品已在媒体库中</p>
            <p className="text-xs text-emerald-700">可直接打开库内详情；下方仍可继续查找更高质量片源。</p>
          </div>
          <Link to={`/media/${item.media_id}`} className="btn-primary shrink-0 px-3 py-2 text-xs">
            查看库内作品
            <ArrowRight size={14} />
          </Link>
        </div>
      )}
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
          {canSubscribe && selectedLibrary && (
            <div className="flex flex-wrap items-end gap-2">
              {enabledRoots.length > 1 && (
                <label className="min-w-56 flex-1 text-xs text-sand-500">
                  追更入库目录
                  <select
                    className="input-base mt-1"
                    value={effectiveSubscriptionRootID}
                    onChange={(event) => setSubscriptionRootID(event.target.value)}
                  >
                    <option value="">选择目录</option>
                    {enabledRoots.map((root) => <option key={root.id} value={root.id}>{root.name || root.path}</option>)}
                  </select>
                </label>
              )}
              <button
                type="button"
                className="btn-outline gap-2"
                disabled={!effectiveSubscriptionRootID || subscribing || subscribed}
                onClick={() => void createSubscription()}
              >
                {subscribing ? <LoaderCircle size={15} className="animate-spin" /> : subscribed ? <Check size={15} /> : <BellPlus size={15} />}
                {subscribed ? '已订阅' : '订阅追更'}
              </button>
            </div>
          )}
          {selectedLibrary && (
            <ResourceSearchDrawer
              key={selectedLibrary.id}
              embedded
              open
              initialQuery={discoverResourceSearchKeyword(item)}
              releaseDate={item.release_date}
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
