import { useEffect, useRef, useState } from 'react'
import { ArrowDown, ArrowUp, Check, Image, LoaderCircle, Plus, Search, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { libraryAPI, type LibraryCoverItem } from '../api/library'
import { imageURL } from '../api/client'
import type { Library } from '../types'
import { apiErrorMessage } from '../pages/adminLibraryPanelModel'

type CoverItem = LibraryCoverItem
type Props = { library: Library; onClose: () => void }

export function LibraryCoverDialog({ library, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [selected, setSelected] = useState<CoverItem[]>([])
  const [loadingConfig, setLoadingConfig] = useState(true)
  const [configError, setConfigError] = useState('')
  const [configFailed, setConfigFailed] = useState(false)
  const [configRequest, setConfigRequest] = useState(0)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState({ q: '', page: 1 })
  const [candidates, setCandidates] = useState<CoverItem[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [browseError, setBrowseError] = useState('')
  const [busy, setBusy] = useState<'preview' | 'save' | null>(null)
  const [error, setError] = useState('')
  const [preview, setPreview] = useState<{ url: string; signature: string } | null>(null)
  const signature = JSON.stringify(selected.map((item) => item.id))

  useEffect(() => {
    const modal = dialog.current
    modal?.showModal()
    return () => modal?.close()
  }, [])

  useEffect(() => {
    let live = true
    setLoadingConfig(true)
    setConfigFailed(false)
    setConfigError('')
    libraryAPI.cover(library.id).then((config) => {
      if (!live) return
      const items = new Map(config.items?.map((item) => [item.id, item]))
      setSelected(config.media_ids.map((id) => items.get(id) ?? { id, title: `已失效作品 (${id})`, poster_url: '' }))
      setConfigError(config.selection_error ?? '')
    }).catch((err: unknown) => {
      if (live) {
        setConfigFailed(true)
        setConfigError(apiErrorMessage(err, '读取封面设置失败'))
      }
    }).finally(() => { if (live) setLoadingConfig(false) })
    return () => { live = false }
  }, [library.id, configRequest])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setBrowseError('')
    libraryAPI.coverCandidates(library.id, filter, controller.signal).then((result) => {
      if (controller.signal.aborted) return
      setCandidates(result.items)
      setTotal(result.total)
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) setBrowseError(apiErrorMessage(err, '作品列表加载失败'))
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [filter, library.id])

  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview.url) }, [preview])

  const changeSelection = (items: CoverItem[]) => {
    setSelected(items)
    setPreview(null)
    setError('')
    setConfigError('')
  }
  const move = (index: number, offset: number) => {
    const items = [...selected]
    ;[items[index], items[index + offset]] = [items[index + offset], items[index]]
    changeSelection(items)
  }
  const generatePreview = async () => {
    setBusy('preview')
    setError('')
    setPreview(null)
    try {
      const blob = await libraryAPI.previewCover(library.id, selected.map((item) => item.id))
      setPreview({ url: URL.createObjectURL(blob), signature })
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : apiErrorMessage(err, '封面预览失败'))
    } finally { setBusy(null) }
  }
  const save = async () => {
    setBusy('save')
    setError('')
    try {
      await libraryAPI.saveCover(library.id, selected.map((item) => item.id))
      toast.success(selected.length ? '媒体库入口封面已保存' : '已恢复自动封面')
      setBusy(null)
      onClose()
    } catch (err: unknown) {
      setError(apiErrorMessage(err, '保存封面失败'))
      setBusy(null)
    }
  }
  const disabled = busy !== null || loadingConfig || configFailed

  return (
    <dialog ref={dialog} aria-labelledby="library-cover-title" onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} className="m-auto w-[calc(100%-2rem)] max-w-5xl overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-0 text-[var(--app-text)] backdrop:bg-black/50">
      <div className="flex h-[min(740px,calc(100dvh-2rem))] flex-col">
        <header className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-4">
          <div className="min-w-0">
            <h2 id="library-cover-title" className="text-lg font-semibold">设置入口封面</h2>
            <p className="truncate text-sm text-[var(--app-muted)]">{library.name} · 选择 1～4 部作品，排序后预览并保存</p>
          </div>
          <button type="button" aria-label="关闭封面设置" className="icon-button" disabled={!!busy} onClick={onClose}><X size={18} /></button>
        </header>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4 md:grid md:grid-cols-[minmax(0,1fr)_360px] md:overflow-hidden">
          <section className="flex min-h-0 shrink-0 flex-col rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3">
            <form className="flex shrink-0 gap-2" onSubmit={(event) => { event.preventDefault(); setFilter({ q: query.trim(), page: 1 }) }}>
              <input autoFocus aria-label="搜索库内作品" placeholder="搜索库内作品" className="input-base min-w-0 flex-1" value={query} onChange={(event) => setQuery(event.target.value)} />
              <button type="submit" className="btn-outline shrink-0 gap-1" disabled={loading}><Search size={15} />搜索</button>
            </form>
            <p className="py-2 text-xs text-[var(--app-muted)]">点击海报加入组合，最多 4 部。首张作品决定背景主色。</p>
            <div className="max-h-96 min-h-[120px] flex-1 overflow-y-auto md:max-h-none" aria-busy={loading}>
              {loading ? <p className="p-4 text-sm text-[var(--app-muted)]">正在加载作品…</p> : browseError ? (
                <div className="p-3 text-sm"><p role="alert">{browseError}</p><button className="btn-outline mt-2" onClick={() => setFilter({ ...filter })}>重试</button></div>
              ) : candidates.length === 0 ? <p className="p-4 text-sm text-[var(--app-muted)]">没有找到作品。</p> : <div className="grid grid-cols-3 gap-2 sm:grid-cols-5 md:grid-cols-3 lg:grid-cols-5">
                {candidates.map((item) => {
                  const index = selected.findIndex((choice) => choice.id === item.id)
                  const picked = index >= 0
                  return <button key={item.id} type="button" aria-label={item.title} title={item.selection_error || item.title} disabled={disabled || !!item.selection_error || !item.poster_url || (!picked && selected.length >= 4)} aria-pressed={picked} onClick={() => changeSelection(picked ? selected.filter((choice) => choice.id !== item.id) : [...selected, item])} className={`min-w-0 overflow-hidden rounded-lg border text-left transition-colors disabled:opacity-50 ${picked ? 'border-brand-500 bg-[var(--app-brand-soft)] text-[var(--app-brand-text)] ring-1 ring-brand-500' : 'border-[var(--app-border)] bg-[var(--app-panel)] hover:border-brand-500/60'}`}>
                    <div className="relative aspect-[2/3] overflow-hidden bg-[var(--app-panel-soft)]">
                      <CoverPoster item={item} />
                      {!!item.poster_url && !item.selection_error && <span className={`absolute right-1.5 top-1.5 flex h-6 min-w-6 items-center justify-center gap-0.5 rounded-full px-1 text-[10px] font-semibold shadow-sm ${picked ? 'bg-brand-500 text-white' : 'bg-black/60 text-white'}`} aria-hidden="true">{picked ? <><Check size={12} />{index + 1}</> : <Plus size={12} />}</span>}
                    </div>
                    <span className="block truncate px-1.5 py-1.5 text-xs font-medium leading-4">{item.title}</span>
                  </button>
                })}
              </div>}
            </div>
            <div className="mt-2 flex shrink-0 items-center justify-between gap-2 text-xs text-[var(--app-muted)]">
              <span>第 {filter.page} 页 · 共 {total} 部</span>
              <div className="flex gap-2"><button className="btn-outline" disabled={loading || filter.page <= 1} onClick={() => setFilter({ ...filter, page: filter.page - 1 })}>上一页</button><button className="btn-outline" disabled={loading || filter.page * 48 >= total} onClick={() => setFilter({ ...filter, page: filter.page + 1 })}>下一页</button></div>
            </div>
          </section>

          <section className="flex min-h-0 shrink-0 flex-col rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 md:overflow-y-auto">
            <div className="mb-2 flex items-center justify-between text-sm"><span className="font-medium">已选作品 · {selected.length}/4</span><button className="text-xs text-brand-500 disabled:opacity-50" disabled={disabled || !selected.length} onClick={() => changeSelection([])}>恢复自动</button></div>
            <div className="grid shrink-0 grid-cols-2 gap-2">
              {selected.map((item, index) => <div key={item.id} className="flex min-w-0 items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-panel)] p-2 text-sm">
                <span className="relative block aspect-[2/3] w-10 shrink-0 overflow-hidden rounded bg-[var(--app-panel-soft)]"><CoverPoster item={item} /></span>
                <div className="min-w-0 flex-1">
                  <span title={item.title}><span className="mb-1 block text-[10px] text-[var(--app-muted)]">{index + 1}{index === 0 ? ' · 背景取色' : ''}</span><span className="line-clamp-2 min-h-8 break-words text-xs">{item.title}</span></span>
                  <div className="mt-1 flex items-center justify-between">
                    <button className="rounded p-1 disabled:opacity-30" aria-label={`上移 ${item.title}`} disabled={disabled || index === 0} onClick={() => move(index, -1)}><ArrowUp size={14} /></button>
                    <button className="rounded p-1 disabled:opacity-30" aria-label={`下移 ${item.title}`} disabled={disabled || index === selected.length - 1} onClick={() => move(index, 1)}><ArrowDown size={14} /></button>
                    <button className="rounded p-1 disabled:opacity-30" aria-label={`移除 ${item.title}`} disabled={disabled} onClick={() => changeSelection(selected.filter((choice) => choice.id !== item.id))}><X size={14} /></button>
                  </div>
                </div>
              </div>)}
              {!selected.length && <p className="col-span-2 py-3 text-sm text-[var(--app-muted)]">自动选择库内作品生成入口封面。</p>}
            </div>
            <div className="mt-3 flex aspect-video shrink-0 items-center justify-center overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-panel)]">
              {preview ? <img src={preview.url} alt="媒体库入口封面预览" className="h-full w-full object-contain" onError={() => { setPreview(null); setError('预览图片解码失败，请重新生成预览') }} /> : <div className="flex flex-col items-center gap-2 p-3 text-center text-sm text-[var(--app-muted)]">{busy === 'preview' ? <LoaderCircle className="animate-spin" size={24} /> : <Image size={24} />}<span>{busy === 'preview' ? '正在生成预览…' : '生成预览后可查看实际组合效果'}</span></div>}
            </div>
            {configError && <p role="alert" className="mt-2 text-xs text-red-500">{configError}</p>}
            {configFailed && <button className="btn-outline mt-2" onClick={() => setConfigRequest((value) => value + 1)}>重试读取设置</button>}
            {error && <p role="alert" className="mt-2 text-xs text-red-500">{error}</p>}
            <p className="mt-2 text-xs text-[var(--app-muted)]">更改选择或顺序后需重新预览。预览不会保存设置。</p>
            <button className="btn-outline mt-auto w-full justify-center" disabled={disabled} onClick={generatePreview}>{busy === 'preview' ? '正在预览…' : '生成预览'}</button>
          </section>
        </div>

        <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-[var(--app-border)] px-5 py-3">
          <button className="btn-outline" disabled={!!busy} onClick={onClose}>取消</button>
          <button className="btn-primary" disabled={disabled || (selected.length > 0 && preview?.signature !== signature)} onClick={save}>{busy === 'save' ? '正在保存…' : selected.length ? '保存封面' : '保存自动封面'}</button>
        </footer>
      </div>
    </dialog>
  )
}

function CoverPoster({ item }: { item: CoverItem }) {
  const src = imageURL(item.poster_url, item.updated_at, { maxWidth: 320, quality: 82 })
  const [failedURL, setFailedURL] = useState('')
  if (!src || failedURL === src) {
    return <span className="flex h-full w-full flex-col items-center justify-center gap-1 p-1 text-center text-[10px] text-[var(--app-muted)]"><Image size={18} aria-hidden="true" /><span>{src ? '海报加载失败' : '暂无海报'}</span></span>
  }
  return <img src={src} alt={`${item.title}海报`} loading="lazy" decoding="async" referrerPolicy="no-referrer" className="h-full w-full object-contain" onError={() => setFailedURL(src)} />
}
