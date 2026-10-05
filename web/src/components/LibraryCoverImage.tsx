import { useState } from 'react'
import { imageURL } from '../api/client'
import type { Library } from '../types'

export function LibraryCoverImage({ library }: { library: Library }) {
  const url = imageURL(`/api/libraries/${encodeURIComponent(library.id)}/cover/image`, library.updated_at, { maxWidth: 360 })
  const [failedURL, setFailedURL] = useState('')
  return <div className="relative aspect-video w-36 shrink-0 self-center overflow-hidden rounded-2xl bg-[var(--app-panel-soft)]">
    {failedURL === url ? <span className="flex h-full items-center justify-center px-2 text-center text-xs text-[var(--app-muted)]">入口封面加载失败</span> : <img src={url} alt="" loading="lazy" decoding="async" className="h-full w-full object-contain" onError={() => setFailedURL(url)} />}
  </div>
}
