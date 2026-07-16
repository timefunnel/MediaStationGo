import { useEffect, useMemo, useState } from 'react'
import { LoaderCircle, RefreshCw, Save, WandSparkles, X } from 'lucide-react'
import toast from 'react-hot-toast'

import {
  titleCleanupAPI,
  type MediaTitleCleanupPreview,
  type MediaTitleCleanupSuggestion,
} from '../api/titleCleanup'

type AITitleCleanupDialogProps = {
  open: boolean
  libraryID: string
  libraryName: string
  onClose: () => void
  onApplied: () => void | Promise<void>
}

const defaultConfidence = 0.8

export function AITitleCleanupDialog({
  open,
  libraryID,
  libraryName,
  onClose,
  onApplied,
}: AITitleCleanupDialogProps) {
  const [preview, setPreview] = useState<MediaTitleCleanupPreview | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState('')

  const load = async () => {
    if (!libraryID) return
    setLoading(true)
    setError('')
    try {
      const next = await titleCleanupAPI.preview(libraryID)
      setPreview(next)
      setSelected(defaultSelectedItems(next.suggestions))
    } catch (requestError) {
      setPreview(null)
      setSelected(new Set())
      setError(cleanupError(requestError, '生成标题清洗预览失败'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!open) return
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, libraryID])

  const selectedItems = useMemo(
    () => preview?.suggestions.filter((item) => selected.has(item.media_id)) ?? [],
    [preview, selected],
  )

  if (!open) return null

  const toggle = (item: MediaTitleCleanupSuggestion) => {
    const ids = selectionGroupIDs(item, preview?.suggestions ?? [])
    setSelected((current) => {
      const next = new Set(current)
      const shouldSelect = ids.some((id) => !next.has(id))
      ids.forEach((id) => shouldSelect ? next.add(id) : next.delete(id))
      return next
    })
  }

  const apply = async () => {
    if (selectedItems.length === 0) {
      toast.error('请先选择要应用的标题')
      return
    }
    setApplying(true)
    setError('')
    try {
      const result = await titleCleanupAPI.apply(libraryID, selectedItems)
      toast.success(`已应用 ${result.updated} 条标题清洗结果`)
      await onApplied()
      await load()
    } catch (requestError) {
      setError(cleanupError(requestError, '应用标题清洗结果失败'))
    } finally {
      setApplying(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-ink-900/45 px-3 py-5 backdrop-blur-sm">
      <div className="flex max-h-[92vh] w-full max-w-6xl flex-col overflow-hidden rounded-xl border border-gray-200 bg-white shadow-2xl">
        <header className="flex items-start justify-between gap-4 border-b border-gray-200 px-5 py-4">
          <div>
            <div className="flex items-center gap-2">
              <WandSparkles size={19} className="text-brand-500" />
              <h2 className="font-display text-xl font-bold text-gray-900">AI 清洗标题</h2>
            </div>
            <p className="mt-1 text-xs text-gray-500">
              {libraryName} · 同时参考目录名和文件名，应用前不会修改媒体数据
            </p>
          </div>
          <button type="button" className="btn-ghost h-9 w-9 p-0" onClick={onClose} aria-label="关闭">
            <X size={17} />
          </button>
        </header>

        <div className="flex-1 overflow-y-auto p-5">
          {loading && (
            <div className="flex min-h-56 items-center justify-center gap-2 text-sm text-gray-500">
              <LoaderCircle size={18} className="animate-spin" />
              正在分析目录和文件名…
            </div>
          )}

          {!loading && error && (
            <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">
              {error}
            </div>
          )}

          {!loading && preview && (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-gray-600">
                <span>
                  待处理 {preview.candidate_count} · 本批 {preview.batch_count} · 应用后剩余约 {preview.remaining_count}
                </span>
                <span>已选择 {selectedItems.length}</span>
              </div>

              <div className="space-y-2">
                {preview.suggestions.map((item) => {
                  const checked = selected.has(item.media_id)
                  const confidence = Math.round(item.confidence * 100)
                  return (
                    <label
                      key={item.media_id}
                      className={`block cursor-pointer rounded-lg border p-3 transition ${
                        checked ? 'border-brand-300 bg-brand-50/40' : 'border-gray-200 bg-white hover:border-gray-300'
                      }`}
                    >
                      <div className="flex items-start gap-3">
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggle(item)}
                          className="mt-1 h-4 w-4 rounded border-gray-300 text-brand-600"
                        />
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2 text-xs">
                            <span className="rounded bg-gray-100 px-2 py-1 font-semibold text-gray-700">
                              {relationLabel(item.relation)}
                            </span>
                            <span className={confidence >= 80 ? 'text-emerald-600' : 'text-amber-600'}>
                              置信度 {confidence}%
                            </span>
                            {item.year ? <span className="text-gray-500">{item.year}</span> : null}
                          </div>
                          <div className="mt-2 grid gap-2 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] md:items-center">
                            <p className="break-words text-sm text-gray-500">{item.current_title || item.filename}</p>
                            <span className="hidden text-gray-300 md:block">→</span>
                            <p className="break-words text-sm font-semibold text-gray-900">{item.title}</p>
                          </div>
                          <p className="mt-2 break-all text-xs text-gray-500">
                            目录：{item.source_directory || '媒体库根目录'} · 文件：{item.filename || '未知'}
                          </p>
                          {item.reason && <p className="mt-1 text-xs text-gray-400">{item.reason}</p>}
                        </div>
                      </div>
                    </label>
                  )
                })}
              </div>
            </div>
          )}
        </div>

        <footer className="flex flex-wrap justify-end gap-2 border-t border-gray-200 px-5 py-4">
          <button type="button" className="btn-outline px-4" onClick={() => void load()} disabled={loading || applying}>
            <RefreshCw size={16} />
            重新分析
          </button>
          <button type="button" className="btn-primary px-5" onClick={() => void apply()} disabled={loading || applying || selectedItems.length === 0}>
            {applying ? <LoaderCircle size={16} className="animate-spin" /> : <Save size={16} />}
            {applying ? '应用中…' : `应用所选 (${selectedItems.length})`}
          </button>
        </footer>
      </div>
    </div>
  )
}

function defaultSelectedItems(items: MediaTitleCleanupSuggestion[]): Set<string> {
  const selected = new Set<string>()
  const versionGroups = new Map<string, MediaTitleCleanupSuggestion[]>()
  items.forEach((item) => {
    if (item.relation === 'version' && item.group_key) {
      versionGroups.set(item.group_key, [...(versionGroups.get(item.group_key) ?? []), item])
    } else if (item.confidence >= defaultConfidence) {
      selected.add(item.media_id)
    }
  })
  versionGroups.forEach((group) => {
    if (group.length >= 2 && group.every((item) => item.confidence >= defaultConfidence)) {
      group.forEach((item) => selected.add(item.media_id))
    }
  })
  return selected
}

function selectionGroupIDs(item: MediaTitleCleanupSuggestion, items: MediaTitleCleanupSuggestion[]): string[] {
  if (item.relation !== 'version' || !item.group_key) return [item.media_id]
  return items
    .filter((candidate) => candidate.relation === 'version' && candidate.group_key === item.group_key)
    .map((candidate) => candidate.media_id)
}

function relationLabel(relation: MediaTitleCleanupSuggestion['relation']): string {
  if (relation === 'version') return '真实版本'
  if (relation === 'part') return '分段 / 短片'
  return '独立作品'
}

function cleanupError(error: unknown, fallback: string): string {
  const payload = (error as { response?: { data?: { error?: string | { message?: string } } } })?.response?.data?.error
  if (typeof payload === 'string' && payload.trim()) return payload
  if (payload && typeof payload === 'object' && payload.message) return payload.message
  return fallback
}
