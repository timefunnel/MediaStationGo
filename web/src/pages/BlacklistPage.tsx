import { useCallback, useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { blacklistAPI, type BlacklistItem } from '../api/blacklist'
import { MediaCard } from '../components/MediaCard'

export function BlacklistPage() {
  const [items, setItems] = useState<BlacklistItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [removing, setRemoving] = useState<string[]>([])

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setItems(await blacklistAPI.list())
    } catch (err) {
      setError(errorMessage(err, '加载拉黑列表失败'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  const remove = async (id: string) => {
    setRemoving((current) => [...current, id])
    try {
      await blacklistAPI.remove(id)
      setItems((current) => current.filter((item) => item.id !== id))
      toast.success('已解除拉黑，Emby 将恢复显示')
    } catch (err) {
      toast.error(errorMessage(err, '解除拉黑失败'))
    } finally {
      setRemoving((current) => current.filter((value) => value !== id))
    }
  }

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <h1 className="font-display text-3xl font-bold text-ink-600">我的拉黑</h1>
        <p className="text-sm text-sand-500">已拉黑的影片仅对当前账户的 Emby 隐藏；拉黑剧集会同时隐藏整剧的所有季和集。</p>
      </div>
      {loading && <p className="py-8 text-ink-100" role="status">加载中…</p>}
      {error && (
        <div className="glass-panel !border-red-400/30 p-4 text-sm text-red-400" role="alert">
          {error}<button type="button" className="ml-3 underline" onClick={() => void load()}>重试</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="glass-panel space-y-3 p-10 text-center">
          <p className="text-lg text-ink-100">还没有拉黑任何影片</p>
          <p className="text-sm text-sand-500">在媒体详情页点击「加入拉黑」，即可在 Emby 中隐藏。</p>
        </div>
      )}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
        {items.map((item) => (
          <div key={item.id} className="space-y-2">
            {item.media ? <MediaCard media={item.media} /> : (
              <div className="glass-panel flex aspect-[2/3] items-center justify-center p-4 text-center text-sm text-sand-500">媒体已移除或无权查看</div>
            )}
            <button type="button" className="btn-outline w-full" disabled={removing.includes(item.id)} onClick={() => void remove(item.id)}>
              {removing.includes(item.id) ? '正在解除…' : item.scope === 'series' ? '解除整剧拉黑' : '解除拉黑'}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

function errorMessage(err: unknown, fallback: string): string {
  return (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? fallback
}
