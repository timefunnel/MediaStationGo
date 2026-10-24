import { useEffect, useState } from 'react'
import { Ban } from 'lucide-react'
import toast from 'react-hot-toast'

import { blacklistAPI } from '../api/blacklist'
import type { Media } from '../types'

export function MediaBlacklistButton({ media }: { media: Media }) {
  const [blocked, setBlocked] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(false)
  const [reload, setReload] = useState(0)
  const series = Boolean(media.series_id || media.season_num > 0 || media.episode_num > 0)

  useEffect(() => {
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
  }, [media.id, reload])

  const toggle = async () => {
    if (blocked === null || busy) return
    setBusy(true)
    try {
      await blacklistAPI.set(media.id, !blocked)
      setBlocked(!blocked)
      toast.success(blocked ? '已解除拉黑，Emby 将恢复显示' : series ? '已拉黑整部剧，Emby 将隐藏所有季和集' : '已加入我的拉黑，Emby 将隐藏该影片')
    } catch (err) {
      toast.error(errorMessage(err, '拉黑操作失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <button type="button" className={`btn-outline gap-2 ${blocked ? '!border-red-200 !bg-red-50 !text-red-600' : ''}`}
      disabled={busy || (blocked === null && !error)} aria-pressed={blocked === true}
      onClick={() => error ? setReload((value) => value + 1) : void toggle()}>
      <Ban size={14} />
      <span>{error ? '重试拉黑状态' : blocked === null ? '加载拉黑状态…' : busy ? '正在保存…' : blocked ? (series ? '解除整剧拉黑' : '解除拉黑') : (series ? '拉黑整部剧' : '加入拉黑')}</span>
    </button>
  )
}

function errorMessage(err: unknown, fallback: string): string {
  return (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? fallback
}
