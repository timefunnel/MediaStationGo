import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertTriangle, Database, LoaderCircle, Pause, Play, RefreshCw, Save } from 'lucide-react'
import toast from 'react-hot-toast'

import { mediaProbeQueueAPI, type ProbeQueueConfig, type ProbeQueuePlan, type ProbeQueueSnapshot, type ProbeQueueStatus } from '../api/mediaProbeQueue'
import { libraryAPI } from '../api/library'
import type { Library } from '../types'

import { MediaProbeQueueTable } from './MediaProbeQueueTable'
import { probeQueueFailureLabel, probeQueueItemTitle, probeQueueStatusLabels as statusLabels } from './mediaProbeQueueModel'

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
  const [retryTitle, setRetryTitle] = useState('')
  const lastRiskTime = useRef<number | undefined>(undefined)

  useEffect(() => {
    if (!snapshot) return
    const alert = snapshot.risk_alert
    const time = alert ? Date.parse(alert.finished_at || alert.updated_at) : 0
    if (lastRiskTime.current !== undefined && time > lastRiskTime.current && alert) {
      toast.error(`${probeQueueFailureLabel(alert)}：${probeQueueItemTitle(alert)}。本轮已停止，请先处理上游限制。`, { duration: 8000 })
    }
    lastRiskTime.current = Math.max(lastRiskTime.current ?? 0, time)
  }, [snapshot])

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
      {!snapshot && !error && <p className="flex items-center gap-2 text-sm text-sand-500"><LoaderCircle size={16} className="animate-spin" />正在加载队列…</p>}

      {snapshot && draft && <>
        <div className="flex flex-wrap gap-2 text-xs">
          {(Object.entries(statusLabels) as [ProbeQueueStatus, string][]).map(([key, label]) => (
            <span key={key} className="rounded-lg border border-gray-200 px-2 py-1">{label} {snapshot.counts[key] ?? 0}</span>
          ))}
        </div>
        <p className="text-sm text-ink-100" aria-live="polite">{snapshot.message}</p>
        {snapshot.collect_error && <p role="alert" className="text-sm text-red-500">自动入队失败：{snapshot.collect_error}</p>}
        {snapshot.risk_alert && <div role="alert" className="space-y-2 rounded-xl border border-red-400/50 bg-red-50/20 p-4 text-sm">
          <p className="flex items-center gap-2 font-semibold text-red-500"><AlertTriangle size={17} />已记录{probeQueueFailureLabel(snapshot.risk_alert)}</p>
          <p className="text-ink-600">{probeQueueItemTitle(snapshot.risk_alert)} · {new Date(snapshot.risk_alert.finished_at || snapshot.risk_alert.updated_at).toLocaleString()}</p>
          {(snapshot.risk_alert.http_status || snapshot.risk_alert.provider_code) && <p className="text-xs text-red-500">{snapshot.risk_alert.http_status ? `HTTP ${snapshot.risk_alert.http_status}` : ''}{snapshot.risk_alert.provider_code ? ` 上游码 ${snapshot.risk_alert.provider_code}` : ''}</p>}
          <p className="break-words text-xs text-red-500">{snapshot.risk_alert.error}</p>
          <p className="text-xs text-ink-100">该任务曾收到上游风控或限流提示。发生失败时会停止当轮，不会自动重试；请先处理上游限制，再手动选择需要重试的任务。</p>
        </div>}

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
          <h3 className="text-sm font-semibold text-ink-600">{plan.retry_media_id ? `仅重试此项：${retryTitle}` : `本轮最多 ${plan.count} 部`} · 并发 {plan.concurrency}</h3>
          {plan.retry_media_id && <p className="text-sm text-ink-100">本次固定执行这一条失败任务；其他待处理影片不会加入此次重试。</p>}
          <p className="text-sm text-ink-100">OpenList {plan.openlist_count} 部、其他远程 {plan.remote_count} 部、本地 {plan.local_count} 部。OpenList 正常取链预计 {plan.openlist_count} 次，底层 115 文件查询及签发预计约 {plan.openlist_count}～{plan.openlist_count * 2} 次；缓存命中可能更少，Token 刷新或缓存恢复可能额外调用。</p>
          <p className="text-xs text-sand-600">目标：{plan.targets.join('；')}。每项最长 {plan.config.timeout_seconds} 秒，结束后间隔 {plan.config.interval_seconds} 秒，本轮最长 {plan.config.run_seconds} 秒。</p>
          <p className="text-xs leading-6 text-sand-600">{plan.notice}</p>
          <p className="text-xs text-sand-500">本轮预览有效至 {new Date(plan.expires_at).toLocaleTimeString()}；新入队影片留待下一轮。</p>
          <div className="flex gap-2">
            <button type="button" disabled={busy || dirty || Date.now() >= Date.parse(plan.expires_at)} className="neon-button px-3 py-2 text-xs disabled:opacity-50" onClick={() => void act(async () => { await mediaProbeQueueAPI.start(plan.id); setPlan(null) }, plan.retry_media_id ? '已启动这一条失败任务的重试' : '已启动已确认的一轮')}>{plan.retry_media_id ? '确认仅重试此项' : '确认范围并启动本轮'}</button>
            <button type="button" className="btn-outline px-3 py-2 text-xs" onClick={() => setPlan(null)}>取消预览</button>
          </div>
        </div>}

        <div className="flex items-center justify-between gap-3">
          <label className="text-xs text-sand-600">任务状态 <select aria-label="筛选队列状态" className="ml-2 rounded-lg border border-gray-200 bg-white p-2" value={status} onChange={(e) => { setStatus(e.target.value); setPage(1) }}><option value="">全部</option>{Object.entries(statusLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
          <span className="text-xs text-sand-500">共 {snapshot.total} 项</span>
        </div>
        <p className="text-xs text-sand-500">下方保留全部媒体库的队列历史；本轮执行只选取配置范围内的待处理影片。</p>
        <MediaProbeQueueTable items={snapshot.items} disabled={disabled || dirty}
          onRetry={(item) => void act(async () => { const next = await mediaProbeQueueAPI.retryPlan(item.media_id); setRetryTitle(probeQueueItemTitle(item)); setPlan(next) })}
          onRequeue={(id) => void act(async () => { await mediaProbeQueueAPI.update(id, 'retry'); setPlan(null) }, '已重新入队，需在下一轮预览确认后执行')}
          onCancel={(id) => void act(async () => { await mediaProbeQueueAPI.update(id, 'cancel'); setPlan(null) })} />
        <div className="flex items-center justify-end gap-3 text-xs text-sand-600"><button type="button" disabled={page <= 1} className="disabled:opacity-40" onClick={() => setPage((value) => value - 1)}>上一页</button><span>第 {page} 页</span><button type="button" disabled={page * 50 >= snapshot.total} className="disabled:opacity-40" onClick={() => setPage((value) => value + 1)}>下一页</button></div>
      </>}
    </section>
  )
}
