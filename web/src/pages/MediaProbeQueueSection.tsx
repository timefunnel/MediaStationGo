import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Database, LoaderCircle, Pause, Play, RefreshCw, Save } from 'lucide-react'
import toast from 'react-hot-toast'

import { mediaProbeQueueAPI, type ProbeQueueConfig, type ProbeQueuePlan, type ProbeQueueSnapshot, type ProbeQueueStatus } from '../api/mediaProbeQueue'
import { libraryAPI } from '../api/library'
import type { Library } from '../types'
import { MediaProbeExchangeSection } from './MediaProbeExchangeSection'

const statusLabels: Record<ProbeQueueStatus, string> = {
  pending: '待处理', running: '运行中', completed: '已完成', partial: '部分信息未知',
  failed: '失败', canceled: '已取消', interrupted: '已中断',
}

function errorMessage(cause: unknown): string {
  return (cause as { response?: { data?: { error?: string } } })?.response?.data?.error
    || (cause instanceof Error ? cause.message : '队列操作失败')
}

export function MediaProbeQueueSection() {
  const [snapshot, setSnapshot] = useState<ProbeQueueSnapshot | null>(null)
  const [draft, setDraft] = useState<ProbeQueueConfig | null>(null)
  const [dirty, setDirty] = useState(false)
  const [libraries, setLibraries] = useState<Library[]>([])
  const [libraryError, setLibraryError] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  const [plan, setPlan] = useState<ProbeQueuePlan | null>(null)

  useEffect(() => {
    if (snapshot && !dirty) setDraft(snapshot.config)
  }, [snapshot, dirty])

  const refresh = useCallback(async () => {
    const data = await mediaProbeQueueAPI.snapshot(status, page)
    setSnapshot(data)
    setDraft((current) => current ?? data.config)
    setError('')
  }, [status, page])

  useEffect(() => {
    let active = true
    const tick = async () => {
      try {
        const data = await mediaProbeQueueAPI.snapshot(status, page)
        if (active) {
          setSnapshot(data)
          setDraft((current) => current ?? data.config)
          setError('')
        }
      } catch (cause) { if (active) setError(errorMessage(cause)) }
    }
    void tick()
    const timer = window.setInterval(() => void tick(), 3000)
    return () => { active = false; window.clearInterval(timer) }
  }, [status, page])

  useEffect(() => {
    let active = true
    libraryAPI.list({ includeHidden: true }).then((items) => {
      if (active) setLibraries(items)
    }).catch((cause) => { if (active) setLibraryError(errorMessage(cause)) })
    return () => { active = false }
  }, [])

  const change = (values: Partial<ProbeQueueConfig>) => {
    setDraft((current) => current ? { ...current, ...values } : current)
    setDirty(true)
    setPlan(null)
  }

  const act = async (operation: () => Promise<unknown>, message?: string) => {
    if (busy) return
    setBusy(true)
    try {
      await operation()
      if (message) toast.success(message)
      await refresh()
    } catch (cause) { toast.error(errorMessage(cause)) }
    finally { setBusy(false) }
  }

  const running = snapshot?.running ?? false
  const disabled = busy || running

  return (
    <section className="glass-panel space-y-4" aria-labelledby="media-probe-queue-title">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="media-probe-queue-title" className="flex items-center gap-2 font-display text-lg font-semibold text-ink-600"><Database size={18} />文件信息队列</h2>
          <p className="mt-1 text-xs text-sand-500">自动发现缺少基础信息的影片并入队；入队只查数据库，执行前预览并确认本轮范围。</p>
        </div>
        <span className={running ? 'text-sm text-brand-500' : 'text-sm text-sand-500'}>{running ? '正在执行本轮' : '执行已暂停'}</span>
      </div>

      {error && <p role="alert" className="text-sm text-red-500">队列加载失败：{error}。已有数据可能过期。</p>}
      {snapshot && <MediaProbeExchangeSection disabled={disabled || dirty} libraryIDs={snapshot.config.library_ids} onImported={refresh} />}
      {!snapshot && !error && <p className="flex items-center gap-2 text-sm text-sand-500"><LoaderCircle size={16} className="animate-spin" />正在加载队列…</p>}

      {snapshot && draft && <>
        <div className="flex flex-wrap gap-2 text-xs">
          {(Object.entries(statusLabels) as [ProbeQueueStatus, string][]).map(([key, label]) => (
            <span key={key} className="rounded-lg border border-gray-200 px-2 py-1">{label} {snapshot.counts[key] ?? 0}</span>
          ))}
        </div>
        <p className="text-sm text-ink-100" aria-live="polite">{snapshot.message}</p>
        {snapshot.collect_error && <p role="alert" className="text-sm text-red-500">自动入队失败：{snapshot.collect_error}</p>}

        <details className="rounded-xl border border-gray-200 p-3">
          <summary className="cursor-pointer text-sm font-medium text-ink-600">队列配置{dirty ? ' · 有未保存更改' : ''}</summary>
          <fieldset disabled={disabled} className="mt-4 space-y-4 disabled:opacity-60">
            <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={draft.auto_collect} onChange={(e) => change({ auto_collect: e.target.checked })} />自动将范围内缺少基础信息的影片入队（含历史影片）</label>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {([
                ['interval_seconds', '任务结束后间隔（秒）', 5, 3600],
                ['timeout_seconds', '单项超时（秒）', 5, 60],
                ['batch_size', '每轮最多影片数', 1, 100],
                ['run_seconds', '本轮最长运行（秒）', 10, 3600],
              ] as const).map(([key, label, min, max]) => (
                <label key={key} className="space-y-1 text-xs text-sand-600">{label}<input type="number" min={min} max={max} step={1} value={draft[key]} onChange={(e) => change({ [key]: Number(e.target.value) })} className="block w-full rounded-lg border border-gray-200 bg-white p-2 text-sm text-ink-600" /></label>
              ))}
            </div>
            <div className="space-y-2 text-sm">
              <p>媒体库范围：{draft.library_ids.length === 0 ? '全部媒体库' : `已选 ${draft.library_ids.length} 个`}（不勾选表示全部）</p>
              {libraryError && <p role="alert" className="text-red-500">媒体库列表加载失败：{libraryError}</p>}
              <div className="flex flex-wrap gap-x-4 gap-y-2">
                {libraries.map((library) => <label key={library.id} className="flex items-center gap-1.5"><input type="checkbox" checked={draft.library_ids.includes(library.id)} onChange={(e) => change({ library_ids: e.target.checked ? [...draft.library_ids, library.id] : draft.library_ids.filter((id) => id !== library.id) })} />{library.name}</label>)}
              </div>
            </div>
            <p className="text-xs text-sand-500">队列串行执行，并受全局 FFprobe 并发上限约束。每轮只处理确认时的影片；失败即停，无自动重试，重启后保持暂停。播放中的同片探测会优先复用。</p>
            <div className="flex gap-2">
              <button type="button" disabled={!dirty || disabled} className="btn-outline gap-1.5 px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(async () => { await mediaProbeQueueAPI.saveConfig(draft); setDirty(false); setPlan(null) }, '队列配置已保存')}><Save size={14} />保存配置</button>
              {dirty && <button type="button" className="btn-outline px-3 py-2 text-xs" onClick={() => { setDraft(snapshot.config); setDirty(false); setPlan(null) }}>撤销更改</button>}
            </div>
          </fieldset>
        </details>

        <div className="flex flex-wrap gap-2">
          <button type="button" disabled={disabled || dirty} className="btn-outline gap-1.5 px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(async () => { const result = await mediaProbeQueueAPI.collect(); setPlan(null); toast.success(`新增或更新 ${result.added} 项队列记录`) })}><RefreshCw size={14} />刷新缺失信息入队</button>
          <button type="button" disabled={disabled || dirty} className="neon-button gap-1.5 px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(async () => setPlan(await mediaProbeQueueAPI.plan()))}><Play size={14} />预览本轮执行范围</button>
          {running && <button type="button" disabled={busy} className="btn-outline gap-1.5 px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(() => mediaProbeQueueAPI.pause(), '已请求暂停，当前读取将被取消')}><Pause size={14} />暂停并停止当前任务</button>}
        </div>

        {plan && !running && <div className="space-y-3 rounded-xl border border-primary-400/40 bg-brand-50/30 p-4" aria-label="本轮执行范围">
          <h3 className="text-sm font-semibold text-ink-600">本轮最多 {plan.count} 部 · 并发 {plan.concurrency}</h3>
          <p className="text-sm text-ink-100">OpenList {plan.openlist_count} 部、其他远程 {plan.remote_count} 部、本地 {plan.local_count} 部。OpenList 正常取链预计 {plan.openlist_count} 次，底层 115 文件查询及签发预计约 {plan.openlist_count}～{plan.openlist_count * 2} 次；缓存命中可能更少，Token 刷新或缓存恢复可能额外调用。</p>
          <p className="text-xs text-sand-600">目标：{plan.targets.join('；')}。每项最长 {plan.config.timeout_seconds} 秒，结束后间隔 {plan.config.interval_seconds} 秒，本轮最长 {plan.config.run_seconds} 秒。</p>
          <p className="text-xs leading-6 text-sand-600">{plan.notice}</p>
          <p className="text-xs text-sand-500">本轮预览有效至 {new Date(plan.expires_at).toLocaleTimeString()}；新入队影片留待下一轮。</p>
          <div className="flex gap-2">
            <button type="button" disabled={busy || dirty || Date.now() >= Date.parse(plan.expires_at)} className="neon-button px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(async () => { await mediaProbeQueueAPI.start(plan.id); setPlan(null) }, '已启动已确认的一轮')}>确认范围并启动本轮</button>
            <button type="button" className="btn-outline px-3 py-2 text-xs" onClick={() => setPlan(null)}>取消预览</button>
          </div>
        </div>}

        <div className="flex items-center justify-between gap-3">
          <label className="text-xs text-sand-600">任务状态 <select aria-label="筛选队列状态" className="ml-2 rounded-lg border border-gray-200 bg-white p-2" value={status} onChange={(e) => { setStatus(e.target.value); setPage(1) }}><option value="">全部</option>{Object.entries(statusLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
          <span className="text-xs text-sand-500">共 {snapshot.total} 项</span>
        </div>
        <p className="text-xs text-sand-500">下方保留全部媒体库的队列历史；本轮执行只选取配置范围内的待处理影片。</p>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[680px] text-left text-sm">
            <thead className="text-xs text-sand-500"><tr><th className="py-2">影片</th><th>状态 / 次数</th><th>结果</th><th>更新时间</th><th>操作</th></tr></thead>
            <tbody>{snapshot.items.map((item) => <tr key={item.media_id} className="border-t border-gray-200 align-top">
              <td className="max-w-xs py-3 pr-3"><Link to={`/media/${item.media_id}`} className="text-brand-500 hover:underline">{item.title}</Link></td>
              <td className="py-3 pr-3">{statusLabels[item.status]} · {item.attempts}</td>
              <td className="max-w-xs break-words py-3 pr-3 text-xs">{item.error || (item.status === 'partial' ? '已探测并保存可用信息；部分基础字段仍未知，不自动重复探测' : item.status === 'completed' ? '信息已保存或复用现有结果' : '—')}</td>
              <td className="whitespace-nowrap py-3 pr-3 text-xs text-sand-500">{new Date(item.updated_at).toLocaleString()}</td>
              <td className="py-3">{item.status === 'pending' ? <button type="button" disabled={disabled} className="text-xs text-sand-600 disabled:opacity-50" onClick={() => void act(async () => { await mediaProbeQueueAPI.update(item.media_id, 'cancel'); setPlan(null) })}>取消</button> : ['failed', 'partial', 'interrupted', 'canceled'].includes(item.status) ? <button type="button" disabled={disabled} className="text-xs text-brand-500 disabled:opacity-50" onClick={() => void act(async () => { await mediaProbeQueueAPI.update(item.media_id, 'retry'); setPlan(null) }, '已重新入队，需在下一轮预览确认后执行')}>重新入队</button> : '—'}</td>
            </tr>)}</tbody>
          </table>
          {snapshot.items.length === 0 && <p className="py-6 text-sm text-sand-500">当前筛选下没有任务。</p>}
        </div>
        <div className="flex items-center justify-end gap-3 text-xs text-sand-600"><button type="button" disabled={page <= 1} className="disabled:opacity-40" onClick={() => setPage((value) => value - 1)}>上一页</button><span>第 {page} 页</span><button type="button" disabled={page * 50 >= snapshot.total} className="disabled:opacity-40" onClick={() => setPage((value) => value + 1)}>下一页</button></div>
      </>}
    </section>
  )
}
