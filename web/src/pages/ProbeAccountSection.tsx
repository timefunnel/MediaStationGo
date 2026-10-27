import { useCallback, useEffect, useState } from 'react'
import { api, LONG_REQUEST_TIMEOUT } from '../api/client'

interface AccountView {
  available: boolean; configured: boolean; account_id?: string; pending_account_id?: string
  session_id?: string; phase: string; image?: string; expires_at?: string; requests: number; error?: string
}
const base = '/admin/media-probe-queue/campaign/account'
const waiting = (view: AccountView | null) => view?.phase === 'cookie_wait' || view?.phase === 'open_wait'
const message = (cause: unknown) => (cause as { response?: { data?: { error?: string } } })?.response?.data?.error || (cause instanceof Error ? cause.message : 'B 账号授权失败')

export function ProbeAccountSection({ disabled, onChanged, onAvailabilityChange }: {
  disabled: boolean; onChanged: () => void; onAvailabilityChange: (ready: boolean) => void
}) {
  const [view, setView] = useState<AccountView | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [polling, setPolling] = useState(false)
  const [error, setError] = useState('')
  const active = waiting(view)
  const refresh = useCallback(async () => {
    const { data } = await api.get<AccountView>(base)
    setView(data)
  }, [])
  useEffect(() => { void refresh().catch(cause => setError(message(cause))) }, [refresh])
  useEffect(() => { onAvailabilityChange(Boolean(view?.configured && !active)) }, [view?.configured, active, onAvailabilityChange])
  const sessionID = view?.session_id
  const phase = view?.phase
  useEffect(() => {
    if (!sessionID || !active || disabled || busy || error) return
    let disposed = false
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const poll = async () => {
      setPolling(true)
      try {
        const { data } = await api.post<AccountView>(`${base}/poll`, { session_id: sessionID }, { signal: controller.signal, timeout: LONG_REQUEST_TIMEOUT })
        if (disposed) return
        setView(data)
        if (data.phase === 'ready') onChanged()
        if (waiting(data)) timer = setTimeout(() => void poll(), 5000)
      } catch (cause) {
        if (disposed) return
        setError(message(cause))
        // This reads node state only; it never retries a 115 request.
        try { await refresh() } catch (refreshCause) { if (!disposed) setError(`${message(cause)}；状态读取失败：${message(refreshCause)}`) }
      } finally { if (!disposed) setPolling(false) }
    }
    timer = setTimeout(() => void poll(), 5000)
    return () => { disposed = true; clearTimeout(timer); controller.abort(); setPolling(false) }
  }, [sessionID, phase, active, disabled, busy, error, refresh, onChanged])

  const action = async (name: 'start' | 'cancel' | 'logout') => {
    if (busy || polling) return
    setBusy(true); setError('')
    try {
      const { data } = await api.post<AccountView>(`${base}/${name}`, name === 'cancel' ? { session_id: sessionID } : { confirmed: true }, { timeout: LONG_REQUEST_TIMEOUT })
      setView(data); setConfirmed(false); onChanged()
    } catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }
  return <div className="space-y-3 rounded-lg border border-gray-200 p-3 text-xs leading-6 text-sand-600" aria-label="B 账号授权">
    <p className="font-semibold text-ink-600">B 账号：{view?.configured ? view.account_id : '尚未登录'}</p>
    {view && !view.available && <p>独立节点尚未连接，连接配置由管理员完成。</p>}
    {view?.error && <p role="alert" className="text-red-500">{view.error}</p>}
    {error && <p role="alert" className="text-red-500">{error}</p>}
    {active ? <>
      <p>{phase === 'cookie_wait' ? '第 1 / 2 步：使用 B 账号的 115 手机客户端扫码，确认网页登录。' : `第 2 / 2 步：继续使用 B 账号 ${view?.pending_account_id || ''} 扫码，确认开放平台授权。`}</p>
      {view?.image && <img src={view.image} alt="B 账号授权二维码" className="h-48 w-48 rounded bg-white p-2" />}
      <p>两次扫码必须是同一个 B 账号。两项授权都成功后保存。已请求 {view?.requests} / 48 次；截止 {view?.expires_at ? new Date(view.expires_at).toLocaleTimeString() : ''}。</p>
      <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy || polling} onClick={() => void action('cancel')}>{polling ? '正在查询，请稍候…' : '取消授权'}</button>
    </> : <>
      <p>登录需要两次扫码：网页登录用于自动转存，开放平台授权用于目录与取链。Cookie 和 Token 保存在独立节点，页面只显示账号与授权状态。</p>
      <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} disabled={disabled || busy || !view?.available} onChange={event => setConfirmed(event.target.checked)} /><span>同意本次授权最多请求 115 共 48 次：二维码与图片各 2 次、状态查询最多 40 次、授权兑换与身份核验共 4 次；串行、间隔至少 5 秒、最长 5 分钟，遇错停止。目标为 qrcodeapi、passportapi、webapi、proapi.115.com；不访问影片、不刷新 OpenList。仍有账号限流、登录失效及链路失败风险。</span></label>
      <div className="flex flex-wrap gap-2">
        <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy || !view?.available || !confirmed} onClick={() => void action('start')}>{busy ? '处理中…' : view?.configured ? '更新 B 授权' : '登录 B 账号'}</button>
        {view?.configured && <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy} onClick={() => void action('logout')}>清除本地 B 登录</button>}
      </div>
    </>}
    <button type="button" className="text-primary-500 disabled:opacity-50" disabled={busy || polling} onClick={() => void refresh().catch(cause => setError(message(cause)))}>读取授权状态</button>
  </div>
}
