import { Captions } from 'lucide-react'
import { Link } from 'react-router-dom'

import type { SubtitleASRTask } from '../api/subtitles'
import {
  splitSubtitleASRTasks,
  subtitleASRLanguageLabel,
  subtitleASRResultSummary,
  subtitleASRStageLabel,
} from './subtitleASRTaskModel'

type SubtitleASRTasksSectionProps = {
  tasks: SubtitleASRTask[] | null
  error: string
}

export function SubtitleASRTasksSection({ tasks, error }: SubtitleASRTasksSectionProps) {
  const grouped = splitSubtitleASRTasks(tasks ?? [])
  return (
    <section className="glass-panel">
      <div className="mb-4 flex items-center gap-2">
        <Captions size={18} className="text-brand-500" />
        <h2 className="font-display text-lg font-semibold text-ink-600">AI 字幕生产任务</h2>
      </div>
      {error && <p className="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-600">{error}</p>}
      {!error && tasks === null && <p className="text-sand-500">正在加载字幕任务…</p>}
      {!error && tasks !== null && (
        <div className="space-y-5">
          <div>
            <h3 className="mb-2 text-sm font-semibold text-ink-500">运行中</h3>
            <SubtitleASRTaskTable tasks={grouped.active} empty="暂无正在生产的字幕。" />
          </div>
          <div>
            <h3 className="mb-2 text-sm font-semibold text-ink-500">最近完成</h3>
            <SubtitleASRTaskTable tasks={grouped.recent} empty="暂无已结束的字幕任务。" />
          </div>
        </div>
      )}
    </section>
  )
}

function SubtitleASRTaskTable({ tasks, empty }: { tasks: SubtitleASRTask[]; empty: string }) {
  if (tasks.length === 0) return <p className="text-sand-500">{empty}</p>
  return (
    <div className="overflow-x-auto">
      <table className="min-w-[860px] w-full text-left text-sm">
        <thead className="text-xs uppercase tracking-wider text-sand-500">
          <tr>
            <th className="py-2">作品 / 片源</th>
            <th>源语言</th>
            <th>阶段</th>
            <th>状态</th>
            <th>结果</th>
            <th>时间</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.id} className="border-t border-gray-200 align-top">
              <td className="max-w-sm py-2">
                {task.media_available ? (
                  <Link className="font-medium text-brand-600 hover:text-brand-500" to={`/media/${task.media_id}`}>
                    {task.media_title || task.media_filename || task.media_id}
                  </Link>
                ) : (
                  <span className="font-medium text-red-500">媒体记录已不存在</span>
                )}
                <div className="truncate text-xs text-sand-500" title={task.media_filename || task.media_id}>
                  {task.media_filename || task.media_id}
                </div>
              </td>
              <td className="text-ink-100">{subtitleASRLanguageLabel(task.source_language)}</td>
              <td className="text-ink-100">
                <div>{subtitleASRStageLabel(task.stage)}</div>
                {task.progress_total > 0 && (
                  <div className="mt-1 text-xs text-sand-500">{task.progress_current}/{task.progress_total}</div>
                )}
              </td>
              <td>{subtitleASRStatusBadge(task.status)}</td>
              <td className={task.status === 'failed' ? 'max-w-md break-words text-red-600' : 'max-w-md break-words text-ink-100'}>
                {subtitleASRResultSummary(task)}
              </td>
              <td className="whitespace-nowrap text-ink-100">{subtitleASRTaskTime(task)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function subtitleASRStatusBadge(status: SubtitleASRTask['status']) {
  const styles = {
    queued: 'border-gray-300 text-sand-500',
    running: 'border-yellow-400/40 text-yellow-600',
    completed: 'border-emerald-400/40 text-emerald-600',
    failed: 'border-red-400/40 text-red-500',
  }
  const labels = { queued: '排队中', running: '生产中', completed: '已成功', failed: '失败' }
  return <span className={`rounded-lg border px-1.5 py-0.5 text-xs ${styles[status]}`}>{labels[status]}</span>
}

function subtitleASRTaskTime(task: SubtitleASRTask): string {
  const timestamp = task.completed_at || task.updated_at || task.started_at || task.created_at
  return timestamp > 0 ? new Date(timestamp * 1000).toLocaleString() : '-'
}
