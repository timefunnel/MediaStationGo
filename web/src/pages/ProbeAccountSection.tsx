import { useCallback, useEffect, useState } from 'react'
import { api, LONG_REQUEST_TIMEOUT } from '../api/client'

interface AccountView {
  available: boolean; configured: boolean; account_id?: string; pending_account_id?: string
  session_id?: string; phase: string; image?: string; expires_at?: string; requests: number; request_limit: number; error?: string
}
const base = '/admin/media-probe-queue/campaign/account'
const waiting = (view: AccountView | null) => Boolean(view && ['cookie_wait', 'open_prepare', 'open_confirm', 'open_token'].includes(view.phase))
const message = (cause: unknown) => (cause as { response?: { data?: { error?: string } } })?.response?.data?.error || (cause instanceof Error ? cause.message : 'B 账号授权失败')

export function ProbeAccountSection({ disabled, onChanged, onAvailabilityChange }: {
  disabled: boolean; onChanged: () => void; onAvailabilityChange: (ready: boolean) => void
}) {
  const [view, setView] = useState<AccountView | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [cookie, setCookie] = useState('')
  const [importConfirmed, setImportConfirmed] = useState(false)
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

  const action = async (name: 'start' | 'cancel' | 'logout' | 'import-cookie') => {
    if (busy || polling) return
    setBusy(true); setError('')
    const payload = name === 'cancel' ? { session_id: sessionID } : name === 'import-cookie' ? { confirmed: true, cookie: cookie.trim() } : { confirmed: true }
    // Never retain an imported session after submission, including failed requests.
    setCookie(''); setImportConfirmed(false)
    try {
      const { data } = await api.post<AccountView>(`${base}/${name}`, payload, { timeout: LONG_REQUEST_TIMEOUT })
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
      <p>{phase === 'cookie_wait' ? '使用 B 账号的 115 手机客户端扫码并确认，只需扫这一次。' : `B 账号 ${view?.pending_account_id || ''} 网页登录已核验，正在自动完成授权，无需扫码。`}</p>
      {view?.image && <img src={view.image} alt="B 账号授权二维码" className="h-48 w-48 rounded bg-white p-2" />}
      <p>登录与自动授权全部成功后保存。已请求 {view?.requests} / {view?.request_limit} 次；截止 {view?.expires_at ? new Date(view.expires_at).toLocaleTimeString() : ''}。等待扫码状态响应时会保留二维码；连接失败与二维码过期会分别提示。</p>
      <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy || polling} onClick={() => void action('cancel')}>{polling ? phase === 'cookie_wait' ? '正在查询，请稍候…' : '正在授权，请稍候…' : '取消授权'}</button>
    </> : <>
      <p>只需扫码一次，系统随后自动完成同一 B 账号的开放平台授权。Cookie 和 Token 保存在独立节点，页面只显示账号与授权状态。自动授权失败会停止并显示原因。</p>
      <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} disabled={disabled || busy || !view?.available} onChange={event => setConfirmed(event.target.checked)} /><span>同意本次登录及自动授权最多请求 115 共 29 次：二维码与图片各 1 次、状态查询最多 20 次、凭据兑换、身份核验及自动授权共 7 次；串行、间隔至少 5 秒、最长 5 分钟，遇错停止。目标为 qrcodeapi、passportapi、webapi、proapi.115.com；不访问影片、不刷新 OpenList。仍有账号限流、登录失效及链路失败风险。</span></label>
      <div className="flex flex-wrap gap-2">
        <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy || !view?.available || !confirmed} onClick={() => void action('start')}>{busy ? '处理中…' : view?.configured ? '更新 B 授权' : '登录 B 账号'}</button>
        {view?.configured && <button type="button" className="btn-outline px-3 py-2 disabled:opacity-50" disabled={disabled || busy} onClick={() => void action('logout')}>清除本地 B 登录</button>}
      </div>
      <details className="rounded border border-gray-200 p-3">
        <summary className="cursor-pointer font-semibold">已在 115 官网登录 B？导入已有登录</summary>
        <p className="mt-2">浏览器官网的登录不会自动传给独立节点。可在此粘贴该 B 账号的完整 Cookie（包含 UID、CID、SEID），无需再扫码或手工获取 Token。不要将 Cookie 发到聊天或截图中。</p>
        <label className="block">B 官网 Cookie<input type="password" autoComplete="off" spellCheck={false} maxLength={16384} value={cookie} onChange={event => setCookie(event.target.value)} disabled={disabled || busy || !view?.available} className="mt-1 w-full rounded border border-gray-200 p-2" /></label>
        <p>提交后清空输入，不保存在浏览器。Cookie 经本服务的私密连接传给 B 节点；核验网页登录及 Token 均属于同一 B 且不是 A 后才保存，失败保留原凭据。</p>
        <label className="mt-2 flex items-start gap-2"><input type="checkbox" checked={importConfirmed} disabled={disabled || busy || !view?.available} onChange={event => setImportConfirmed(event.target.checked)} /><span>同意导入上述 B 登录，并自动授权最多请求 115 共 6 次：网页登录身份、Open 设备码、自动扫码、自动确认、Token 兑换及 Token 身份各 1 次。目标为 webapi、qrcodeapi、proapi.115.com；串行、间隔至少 5 秒、最长 5 分钟，遇错停止且不重试；不获取二维码图片、不查询扫码状态、不访问影片、不刷新 OpenList。仍有登录失效、限流及账号风控风险。</span></label>
        <button type="button" className="btn-outline mt-2 px-3 py-2 disabled:opacity-50" disabled={disabled || busy || !view?.available || !importConfirmed || !cookie.trim()} onClick={() => void action('import-cookie')}>{busy ? '正在核验…' : '导入 B 登录并自动授权'}</button>
      </details>
    </>}
    <button type="button" className="text-primary-500 disabled:opacity-50" disabled={busy || polling} onClick={() => void refresh().catch(cause => setError(message(cause)))}>读取授权状态</button>
  </div>
}
