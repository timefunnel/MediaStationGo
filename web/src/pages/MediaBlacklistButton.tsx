import { useEffect, useState } from 'react'
import { Ban, LoaderCircle } from 'lucide-react'
import toast from 'react-hot-toast'

import { blacklistAPI } from '../api/blacklist'
import type { Media } from '../types'

export function MediaBlacklistButton({ media, compact = false }: { media: Media; compact?: boolean }) {
  const [blocked, setBlocked] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(false)
  const [reload, setReload] = useState(0)
  const series = Boolean(media.series_id || media.season_num > 0 || media.episode_num > 0)

  useEffect(() => {
    if (compact) return
    let active = true
    setBlocked(null)
    setError(false)
    blacklistAPI.status(media.id).then((value) => {
      if (active) setBlocked(value)
    }).catch((err: unknown) => {
      if (active) {
        setError(true)
        toast.error(errorMessage(err, '拉黑状态加载失败'))
      }
    })
    return () => { active = false }
  }, [compact, media.id, reload])

  const toggle = async () => {
    if (busy || (!compact && blocked === null)) return
    setBusy(true)
    try {
      // Poster grids do not preload one status request per card. Read the
      // current account state only when its shortcut is actually used.
      const current = compact ? await blacklistAPI.status(media.id) : blocked
      await blacklistAPI.set(media.id, !current)
      setBlocked(!current)
      toast.success(current ? '已解除拉黑，Emby 将恢复显示' : series ? '已拉黑整部剧，Emby 将隐藏所有季和集' : '已加入我的拉黑，Emby 将隐藏该影片')
    } catch (err) {
      toast.error(errorMessage(err, '拉黑操作失败'))
    } finally {
      setBusy(false)
    }
  }

  const label = error ? '重试拉黑状态'
    : busy ? '正在保存…'
    : blocked === null ? (compact ? (series ? '拉黑或解除整剧拉黑' : '拉黑或解除拉黑') : '加载拉黑状态…')
    : blocked ? (series ? '解除整剧拉黑' : '解除拉黑')
    : series ? '拉黑整部剧' : '加入拉黑'
  const buttonClass = compact
    ? 'flex h-8 w-8 items-center justify-center rounded-lg border border-white/70 bg-white/90 text-gray-700 shadow-sm backdrop-blur transition hover:bg-red-50 hover:text-red-600 disabled:opacity-50'
    : 'btn-outline gap-2'

  return (
    <button type="button" className={`${buttonClass} ${blocked ? '!border-red-200 !bg-red-50 !text-red-600' : ''}`}
      title={compact ? label : undefined} aria-label={label}
      disabled={busy || (!compact && blocked === null && !error)} aria-pressed={blocked === null ? undefined : blocked}
      onClick={() => error ? setReload((value) => value + 1) : void toggle()}>
      {compact && busy ? <LoaderCircle size={13} className="animate-spin" /> : <Ban size={compact ? 13 : 14} />}
      <span className={compact ? 'sr-only' : undefined}>{label}</span>
    </button>
  )
}

function errorMessage(err: unknown, fallback: string): string {
  return (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? fallback
}
