import type { SubtitleASRTask } from '../api/subtitles'

export function splitSubtitleASRTasks(tasks: SubtitleASRTask[]) {
  return {
    active: tasks.filter((task) => task.status === 'queued' || task.status === 'running'),
    recent: tasks.filter((task) => task.status === 'completed' || task.status === 'failed'),
  }
}

export function subtitleASRStageLabel(stage: SubtitleASRTask['stage']): string {
  return {
    queued: '等待生成',
    starting: '正在启动任务',
    extracting_audio: '正在抽取音轨',
    transcribing: 'SenseVoice 正在识别',
    translating: '正在翻译为简体中文',
    saving: '正在保存字幕',
    completed: 'AI 字幕已生成',
    failed: 'AI 字幕生成失败',
  }[stage] || stage
}

export function subtitleASRLanguageLabel(language: SubtitleASRTask['source_language']): string {
  return {
    auto: '自动识别',
    ja: '日语',
    en: '英语',
    zh: '中文',
    ko: '韩语',
  }[language] || language
}

export function subtitleASRResultSummary(task: SubtitleASRTask): string {
  if (task.status === 'failed') return task.error || '任务失败但未返回错误原因'
  if (task.status !== 'completed') {
    return task.progress_total > 0
      ? `${task.progress_current}/${task.progress_total}`
      : '处理中'
  }
  if (!task.result) return '任务完成但未返回字幕结果'
  return [
    task.result.filename,
    task.result.segment_count > 0 ? `${task.result.segment_count} 个分段` : '',
    task.result.duration > 0 ? `${formatDuration(task.result.duration)}` : '',
  ].filter(Boolean).join(' · ')
}

function formatDuration(seconds: number): string {
  const rounded = Math.round(seconds)
  const hours = Math.floor(rounded / 3600)
  const minutes = Math.floor((rounded % 3600) / 60)
  const remaining = rounded % 60
  return hours > 0
    ? `${hours}:${String(minutes).padStart(2, '0')}:${String(remaining).padStart(2, '0')}`
    : `${minutes}:${String(remaining).padStart(2, '0')}`
}
