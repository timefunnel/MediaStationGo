import { useCallback, useEffect, useState } from 'react'
import { api, LONG_REQUEST_TIMEOUT } from '../api/client'

interface CampaignPlan {
  id: string; digest: string; files: number; batches: number; b_account_id: string
  a_requests_max: number; control_requests_max: number; media_requests_max: number
  media_bytes_max: number; run_seconds_max: number; notice: string; targets: string[]
}
interface CampaignView {
  configured: boolean; plan?: CampaignPlan; status: string; phase?: string
  finished_batches: number; counts: Record<string, number>; error?: string
}
const base = '/admin/media-probe-queue/campaign'
const statusLabels: Record<string, string> = { idle: '尚未启动', planned: '等待启动', running: '运行中', completed: '已完成', stopped: '已停止', interrupted: '服务重启后已停止' }
const phaseLabels: Record<string, string> = { initializing: '初始化', creating_source_share: '准备来源分享', source_ready: '来源已准备', creating_b_staging: '准备 B 暂存目录', running_batch: '转存和补齐', batch_applied: '本批已回填', done: '已结束' }

export function MediaProbeCampaignSection({ disabled, libraryIDs, onImported }: {
  disabled: boolean; libraryIDs: string[]; onImported: () => Promise<void>
}) {
  const [view, setView] = useState<CampaignView | null>(null)
  const [plan, setPlan] = useState<CampaignPlan | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const scopeKey = [...libraryIDs].sort().join(',')
  useEffect(() => { setPlan(null); setConfirmed(false) }, [scopeKey])
  const refresh = useCallback(async () => { const { data } = await api.get<CampaignView>(base); setView(data); return data }, [])
  const report = (cause: unknown) => setError((cause as { response?: { data?: { error?: string } } })?.response?.data?.error || (cause instanceof Error ? cause.message : '独立补齐操作失败'))
  useEffect(() => { void refresh().catch(report) }, [refresh])
  useEffect(() => {
    if (view?.status !== 'running') return
    const timer = setInterval(() => {
      void refresh().then(async next => { if (next.status !== 'running') await onImported() }).catch(report)
    }, 5000)
    return () => clearInterval(timer)
  }, [view?.status, refresh, onImported])
  const perform = async (action: () => Promise<void>) => {
    if (busy) return
    setBusy(true); setError('')
    try { await action() } catch (cause) { report(cause) } finally { setBusy(false) }
  }
  const running = view?.status === 'running'
  return <section className="space-y-3 rounded-xl border border-gray-200 p-4" aria-label="第二账号自动补齐">
    <h4 className="text-sm font-semibold text-ink-600">第二账号自动补齐</h4>
    <p className="text-xs leading-6 text-sand-600">维护好 B 账号后，沿用上方已保存的媒体库选择；未选择时覆盖全部云端库。系统自动准备来源分享与暂存目录，分批转存、补齐并回填。A 账号沿用已有登录，B 账号与探测负载由独立节点管理。</p>
    {view && <p className="text-xs text-sand-600">{statusLabels[view.status] || view.status}{view.phase ? ` · ${phaseLabels[view.phase] || view.phase}` : ''}{view.plan ? ` · 已回填 ${view.finished_batches}/${view.plan.batches} 批` : ''}</p>}
    {view && !view.configured && <p className="text-xs text-amber-600">独立节点尚未配置；节点部署和服务连接由管理员完成。</p>}
    {view?.error && <p role="alert" className="text-xs text-red-500">{view.error}</p>}
    {error && <p role="alert" className="text-xs text-red-500">{error}</p>}
    <div className="flex flex-wrap gap-2">
      <button type="button" className="btn-outline px-3 py-2 text-xs disabled:opacity-50" disabled={disabled || busy || running || !view?.configured} onClick={() => void perform(async () => {
        setPlan(null); setConfirmed(false)
        const { data } = await api.post<CampaignPlan>(`${base}/plan`, { library_ids: libraryIDs }, { timeout: LONG_REQUEST_TIMEOUT })
        setPlan(data); await refresh()
      })}>预览所选范围</button>
      {running && <button type="button" className="btn-outline px-3 py-2 text-xs disabled:opacity-50" disabled={busy} onClick={() => void perform(async () => { await api.post(`${base}/pause`); await refresh() })}>停止任务</button>}
      <button type="button" className="btn-outline px-3 py-2 text-xs disabled:opacity-50" disabled={busy} onClick={() => void perform(async () => { await refresh() })}>刷新状态</button>
    </div>
    {plan && !running && <div className="space-y-2 rounded-lg border border-primary-400/40 p-3 text-xs leading-6 text-sand-600">
      <p>账号 B：{plan.b_account_id} · 待补齐 {plan.files} 个文件 · {plan.batches} 批，每批最多 10 个</p>
      <p>总上限：A 请求 {plan.a_requests_max} 次；控制请求 {plan.control_requests_max} 次（含 A）；媒体读取 {plan.media_requests_max} 次、响应体 {(plan.media_bytes_max / 1024 ** 3).toFixed(2)} GiB；最长 {(plan.run_seconds_max / 3600).toFixed(1)} 小时。实际用量通常低于上限。</p>
      <p>{plan.notice}</p>
      <details><summary className="cursor-pointer">请求目标</summary>{plan.targets.map(target => <p key={target} className="break-words">{target}</p>)}</details>
      <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} />确认此范围和总预算，并在执行期间保持来源文件稳定。</label>
      <button type="button" className="neon-button px-3 py-2 text-xs disabled:opacity-50" disabled={disabled || busy || !confirmed || view?.status !== 'planned' || view.plan?.id !== plan.id} onClick={() => void perform(async () => {
        await api.post(`${base}/start`, { id: plan.id, digest: plan.digest, confirmed: true }); setPlan(null); setConfirmed(false); await refresh()
      })}>启动自动补齐</button>
    </div>}
  </section>
}
