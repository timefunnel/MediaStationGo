import { Link } from 'react-router-dom'
import type { ProbeQueueItem } from '../api/mediaProbeQueue'
import { probeQueueFailureLabel, probeQueueItemTitle, probeQueueResolution, probeQueueResultDetails, probeQueueStatusLabels } from './mediaProbeQueueModel'

interface Props {
  items: ProbeQueueItem[]
  disabled: boolean
  onRetry: (item: ProbeQueueItem) => void
  onRequeue: (id: string) => void
  onCancel: (id: string) => void
}

export function MediaProbeQueueTable({ items, disabled, onRetry, onRequeue, onCancel }: Props) {
  return <div className="overflow-x-auto">
    <table className="w-full min-w-[780px] text-left text-sm">
      <thead className="text-xs text-sand-500"><tr><th className="py-2">影片 / 剧集</th><th>状态</th><th>探测结果</th><th>更新时间</th><th>操作</th></tr></thead>
      <tbody>{items.map((item) => <tr key={item.media_id} className="border-t border-gray-200 align-top">
        <td className="max-w-xs py-3 pr-3">
          <Link to={`/media/${item.media_id}`} className="text-brand-500 hover:underline">{probeQueueItemTitle(item)}</Link>
          {item.file_name && <p className="mt-1 break-all text-xs text-sand-500">{item.file_name}</p>}
        </td>
        <td className="whitespace-nowrap py-3 pr-3">{probeQueueStatusLabels[item.status]}</td>
        <td className="max-w-sm break-words py-3 pr-3 text-xs">
          {item.error && <div className="space-y-1 text-red-500">
            <p className="font-medium">{probeQueueFailureLabel(item)}{item.http_status ? ` · HTTP ${item.http_status}` : ''}{item.provider_code ? ` · 上游码 ${item.provider_code}` : ''}</p>
            <p className="whitespace-pre-wrap">{item.error}</p>
          </div>}
          {item.result && <div className="space-y-1">
            <p className="font-medium text-ink-600">{probeQueueResolution(item.result)}</p>
            <p className="leading-5 text-ink-100">{probeQueueResultDetails(item.result).join(' · ')}</p>
            <p className="text-sand-500">已保存的探测参数{item.status === 'partial' ? '；部分基础字段仍未知' : ''}</p>
          </div>}
          {!item.error && !item.result && <span className="text-sand-500">{['completed', 'partial'].includes(item.status) ? '没有已保存的探测参数' : '尚无探测结果'}</span>}
        </td>
        <td className="whitespace-nowrap py-3 pr-3 text-xs text-sand-500">{new Date(item.updated_at).toLocaleString()}</td>
        <td className="py-3">
          {item.status === 'failed'
            ? <button type="button" disabled={disabled} className="whitespace-nowrap text-xs text-brand-500 disabled:opacity-50" onClick={() => onRetry(item)}>重试此项</button>
            : item.status === 'pending'
              ? <button type="button" disabled={disabled} className="text-xs text-sand-600 disabled:opacity-50" onClick={() => onCancel(item.media_id)}>取消</button>
              : ['partial', 'interrupted', 'canceled'].includes(item.status)
                ? <button type="button" disabled={disabled} className="whitespace-nowrap text-xs text-brand-500 disabled:opacity-50" onClick={() => onRequeue(item.media_id)}>重新入队</button>
                : '—'}
        </td>
      </tr>)}</tbody>
    </table>
    {items.length === 0 && <p className="py-6 text-sm text-sand-500">当前筛选下没有任务。</p>}
  </div>
}
