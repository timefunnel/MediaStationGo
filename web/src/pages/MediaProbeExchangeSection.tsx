import { useState } from 'react'
import { Download, Upload } from 'lucide-react'
import toast from 'react-hot-toast'

import { mediaProbeExchangeAPI, type ProbeExchangePreview } from '../api/mediaProbeExchange'
import { MediaProbeCampaignSection } from './MediaProbeCampaignSection'

const labels: Record<string, string> = {
  completed: '基础信息已补齐', partial: '部分信息未知', unchanged: '无需修改',
  conflict: '字段或来源冲突', mapping_conflict: '文件对应关系冲突',
  failed: '失败', pending: '尚未执行', unsupported: '暂不支持', already_applied: '已导入',
}

const fields: Record<string, string> = {
  duration_sec: '时长（秒）', width: '宽度', height: '高度', video_codec: '视频编码', audio_codec: '音频编码',
  container: '容器', bit_rate: '总码率', video_bit_rate: '视频码率', frame_rate: '帧率', video_profile: '视频规格',
  video_range: '动态范围', video_bit_depth: '位深', audio_bit_rate: '音频码率', audio_channels: '声道数',
  audio_channel_layout: '声道布局', audio_sample_rate: '采样率', media_probe_version: '探测版本',
}

async function exchangeError(cause: unknown): Promise<string> {
  const response = (cause as { response?: { data?: unknown } })?.response?.data
  if (response instanceof Blob) {
    try { return (JSON.parse(await response.text()) as { error?: string }).error || '导出失败' }
    catch { return '导出失败，服务器未返回有效错误信息' }
  }
  return (response as { error?: string })?.error || (cause instanceof Error ? cause.message : '文件信息操作失败')
}

export function MediaProbeExchangeSection({ disabled, libraryIDs, onImported }: {
  disabled: boolean
  libraryIDs: string[]
  onImported: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [bundle, setBundle] = useState<unknown>(null)
  const [preview, setPreview] = useState<ProbeExchangePreview | null>(null)
  const [sourceConfirmed, setSourceConfirmed] = useState(false)
  const [error, setError] = useState('')

  const perform = async (operation: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    setError('')
    try { await operation() }
    catch (cause) { setError(await exchangeError(cause)) }
    finally { setBusy(false) }
  }

  const exportManifest = async () => {
    const data = await mediaProbeExchangeAPI.export(libraryIDs)
    const url = URL.createObjectURL(data)
    const anchor = document.createElement('a')
    try {
      anchor.href = url
      anchor.download = `media-probe-manifest-${new Date().toISOString().replace(/:/g, '-')}.json`
      document.body.appendChild(anchor)
      anchor.click()
    } finally { anchor.remove(); URL.revokeObjectURL(url) }
  }

  const importFile = async (file: File) => {
    setPreview(null)
    setBundle(null)
    setSourceConfirmed(false)
    if (file.size > 64 * 1024 * 1024) throw new Error('结果包不能超过 64 MiB')
    const parsed: unknown = JSON.parse(await file.text())
    const next = await mediaProbeExchangeAPI.preview(parsed)
    setBundle(parsed)
    setPreview(next)
  }

  const apply = async () => {
    if (!preview || !sourceConfirmed || !bundle) return
    const result = await mediaProbeExchangeAPI.apply(bundle, preview.digest)
    setPreview(null)
    setBundle(null)
    setSourceConfirmed(false)
    await onImported()
    toast.success(result.already_applied ? '此结果包已经导入，没有重复修改' : '已导入校验通过的信息；冲突及失败项保持原状态')
  }

  const writeCount = preview?.changes.filter(change => change.after && Object.keys(change.after).length > 0).length ?? 0

  return <div className="space-y-3"><MediaProbeCampaignSection disabled={disabled} libraryIDs={libraryIDs} onImported={onImported} /><details className="rounded-xl border border-gray-200 p-4">
    <summary className="cursor-pointer text-sm font-semibold text-ink-600">高级：手动交换任务文件</summary>
    <div className="mt-3 space-y-3">
      <p className="text-xs leading-6 text-sand-600">手动交换用于离线审查或故障处理。日常使用上方自动补齐入口，无需创建分享、填写目录 ID 或操作任务文件。导出范围沿用已保存的媒体库选择。</p>
      <div className="flex flex-wrap gap-2">
        <button type="button" disabled={disabled || busy} className="btn-outline gap-1.5 px-3 py-2 text-xs disabled:opacity-50" onClick={() => void perform(exportManifest)}><Download size={14} />导出任务清单</button>
        <label className={`btn-outline gap-1.5 px-3 py-2 text-xs ${disabled || busy ? 'pointer-events-none opacity-50' : 'cursor-pointer'}`}><Upload size={14} />导入结果并预览<input type="file" accept="application/json,.json" className="sr-only" disabled={disabled || busy} aria-label="选择独立节点结果包" onChange={event => {
          const file = event.target.files?.[0]
          event.target.value = ''
          if (file) void perform(() => importFile(file))
        }} /></label>
      </div>
      {busy && <p role="status" className="text-xs text-sand-500">正在处理任务文件…</p>}
      {error && <p role="alert" className="text-xs text-red-500">{error}</p>}
      {preview && <div className="space-y-3 rounded-lg border border-primary-400/40 p-3" aria-label="独立节点导入预览">
        <p className="text-sm text-ink-600">{preview.already_applied ? '此结果包已经导入。' : `本次将修改 ${writeCount} 项媒体。`}</p>
        <p className="text-xs text-sand-600">{Object.entries(preview.counts).map(([key, count]) => `${labels[key] || key} ${count}`).join(' · ')}</p>
        <div className="max-h-80 space-y-2 overflow-y-auto text-xs">
          {preview.changes.map(change => <div key={change.media_id} className="rounded border border-gray-200 p-2">
            <p className="break-words">{change.relative_path || change.media_id} · {labels[change.status] || change.status}</p>
            {change.error && <p className="mt-1 text-red-500">{change.error}</p>}
            {change.after && Object.entries(change.after).map(([key, value]) => <p key={key} className="mt-1 text-sand-600">{fields[key] || key}：{String(change.before?.[key] ?? '未知')} → {String(value)}{change.estimated_fields?.includes(key) ? '（估算）' : ''}</p>)}
            {(change.unknown_fields?.length ?? 0) > 0 && <p className="mt-1 text-sand-500">仍未知：{change.unknown_fields?.map(key => fields[key] || key).join('、')}</p>}
          </div>)}
        </div>
        {!preview.already_applied && writeCount > 0 && <>
          <label className="flex items-start gap-2 text-xs leading-5 text-sand-600"><input type="checkbox" checked={sourceConfirmed} onChange={event => setSourceConfirmed(event.target.checked)} />我确认这是原样转存批次，导出后主账号中的原文件没有替换或改名，且已核对上述修改。</label>
          <button type="button" disabled={disabled || busy || !sourceConfirmed} className="neon-button px-3 py-2 text-xs disabled:opacity-50" onClick={() => void perform(apply)}>确认并导入这 {writeCount} 项</button>
        </>}
        <button type="button" className="btn-outline px-3 py-2 text-xs" onClick={() => { setPreview(null); setBundle(null); setSourceConfirmed(false) }}>关闭预览</button>
      </div>}
    </div>
  </details></div>
}
