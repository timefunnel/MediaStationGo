import {
  Building2,
  CalendarDays,
  Clock3,
  ExternalLink,
  Hash,
  LoaderCircle,
  Star,
  Tags,
  UserRound,
  X,
  ZoomIn,
} from 'lucide-react'

import type { DiscoverItem } from '../api/discover'
import { imageURL } from '../api/client'
import {
  discoverItemMetaText,
  discoverItemPeople,
  discoverItemValues,
  discoverPerformerItem,
  discoverReleaseStatus,
} from './discoverDetailModalModel'

export function DiscoverModalHeader({ item, source, onClose }: { item: DiscoverItem; source: string; onClose: () => void }) {
  return (
    <div className="mb-4 flex items-start justify-between gap-3">
      <div>
        <p className="text-xs font-semibold uppercase tracking-widest text-brand-500">{source}</p>
        <h2 className="font-display text-2xl font-bold text-ink-600">{item.title}</h2>
        <p className="mt-1 text-sm text-sand-500">{discoverItemMetaText(item)}</p>
      </div>
      <button className="rounded-full border border-gray-200 p-2 text-ink-50 hover:bg-gray-50" onClick={onClose}>
        <X size={18} />
      </button>
    </div>
  )
}

export function DiscoverArtworkPanel({
  item,
  deferred = false,
  onPreview,
}: {
  item: DiscoverItem
  deferred?: boolean
  onPreview?: () => void
}) {
  const adultArtwork = item.media_type === 'adult'
  if (adultArtwork && deferred) {
    return (
      <div className="aspect-[3/2] overflow-hidden rounded-2xl bg-gray-950">
        <div className="flex h-full items-center justify-center gap-2 text-sm font-medium text-gray-300">
          <LoaderCircle size={18} className="animate-spin" />
          正在加载完整封面…
        </div>
      </div>
    )
  }
  return (
    <div className="space-y-3">
      <div className={`overflow-hidden rounded-2xl ${adultArtwork ? 'bg-gray-950' : 'bg-gray-100'}`}>
        {item.poster_url ? (
          <button
            type="button"
            className="group relative block w-full cursor-zoom-in overflow-hidden text-left"
            aria-label={`查看 ${item.title} 大图`}
            onClick={onPreview}
          >
            <img
              src={imageURL(item.poster_url)}
              alt={item.title}
              className={`${adultArtwork ? 'aspect-[3/2] object-contain' : 'aspect-[2/3] object-cover'} w-full transition duration-200 group-hover:scale-[1.01]`}
            />
            <span className="absolute bottom-3 right-3 inline-flex items-center gap-1.5 rounded-full bg-black/65 px-2.5 py-1.5 text-xs font-medium text-white opacity-90 shadow-lg backdrop-blur-sm transition group-hover:bg-black/80">
              <ZoomIn size={14} />
              查看大图
            </span>
          </button>
        ) : (
          <div className={`flex items-center justify-center text-sand-500 ${adultArtwork ? 'aspect-[3/2]' : 'aspect-[2/3]'}`}>无海报</div>
        )}
      </div>
      {item.backdrop_url && (
        <img src={imageURL(item.backdrop_url)} alt="" className="h-24 w-full rounded-2xl object-cover" />
      )}
    </div>
  )
}

export function DiscoverMetadataPanel({
  item,
  loading,
  error,
  onSelectPerformer,
}: {
  item: DiscoverItem
  loading: boolean
  error: string
  onSelectPerformer?: (item: DiscoverItem) => void
}) {
  const people = discoverItemPeople(item)
  const peopleLabel = item.media_type === 'adult' ? '女优' : '演员'
  const genres = discoverItemValues(item.genres).filter((value) => !['adult', item.source?.toLowerCase()].includes(value.toLowerCase()))
  const releaseStatus = discoverReleaseStatus(item.release_date)

  return (
    <section className="space-y-4 border-y border-gray-200 py-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-ink-600">作品资料</h3>
        {loading && (
          <span className="inline-flex items-center gap-1.5 text-xs text-sand-500">
            <LoaderCircle size={14} className="animate-spin" />
            正在补充 JavDB 详情…
          </span>
        )}
      </div>

      <div className="grid gap-x-6 gap-y-3 sm:grid-cols-2 xl:grid-cols-3">
        {item.original_name?.trim() && (
          <MetadataValue icon={<Hash size={15} />} label="番号" value={item.original_name.trim()} mono />
        )}
        {item.release_date?.trim() && (
          <MetadataValue
            icon={<CalendarDays size={15} />}
            label="发行日期"
            value={item.release_date.trim()}
            suffix={releaseStatus === 'upcoming' ? '未发行' : undefined}
          />
        )}
        {item.duration_minutes && item.duration_minutes > 0 && (
          <MetadataValue icon={<Clock3 size={15} />} label="时长" value={`${item.duration_minutes} 分钟`} />
        )}
        {item.maker?.trim() && (
          <MetadataValue icon={<Building2 size={15} />} label="片商" value={item.maker.trim()} />
        )}
        {item.rating && item.rating > 0 && (
          <MetadataValue icon={<Star size={15} />} label="评分" value={item.rating.toFixed(1)} />
        )}
      </div>

      {people.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold text-sand-500">
            <UserRound size={14} />
            {peopleLabel}
          </div>
          <div className="flex flex-wrap gap-2">
            {people.map((person) => {
              const selectable = Boolean(onSelectPerformer && person.source_id?.trim())
              const className = 'inline-flex min-h-8 items-center gap-1.5 rounded-md border border-rose-100 bg-rose-50 px-2.5 py-1 text-xs font-medium text-rose-700'
              return selectable ? (
                <button
                  key={`${person.source || 'person'}-${person.source_id || person.name}`}
                  type="button"
                  className={`${className} hover:border-rose-200 hover:bg-rose-100`}
                  onClick={() => onSelectPerformer?.(discoverPerformerItem(person))}
                >
                  <UserRound size={13} />
                  {person.name}
                </button>
              ) : (
                <span key={person.name} className={className}>
                  <UserRound size={13} />
                  {person.name}
                </span>
              )
            })}
          </div>
        </div>
      )}

      {genres.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold text-sand-500">
            <Tags size={14} />
            类别
          </div>
          <div className="flex flex-wrap gap-2">
            {genres.map((genre) => (
              <span key={genre} className="rounded-md bg-gray-100 px-2.5 py-1 text-xs text-ink-100">
                {genre}
              </span>
            ))}
          </div>
        </div>
      )}

      {error && (
        <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
          {error}，当前仅展示列表已有资料。
        </p>
      )}

      {item.provider_url?.trim() && (
        <a
          href={item.provider_url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 text-xs font-medium text-brand-600 hover:text-brand-700"
        >
          <ExternalLink size={14} />
          查看原始资料
        </a>
      )}
    </section>
  )
}

function MetadataValue({
  icon,
  label,
  value,
  suffix,
  mono = false,
}: {
  icon: React.ReactNode
  label: string
  value: string
  suffix?: string
  mono?: boolean
}) {
  return (
    <div className="flex min-w-0 items-start gap-2">
      <span className="mt-0.5 shrink-0 text-brand-500">{icon}</span>
      <div className="min-w-0">
        <p className="text-xs text-sand-500">{label}</p>
        <div className="flex flex-wrap items-center gap-1.5">
          <span className={`break-words text-sm font-medium text-ink-600 ${mono ? 'font-mono' : ''}`}>{value}</span>
          {suffix && <span className="rounded bg-amber-100 px-1.5 py-0.5 text-2xs font-semibold text-amber-800">{suffix}</span>}
        </div>
      </div>
    </div>
  )
}

export function DiscoverOverviewPanel({ overview }: { overview?: string }) {
  if (!overview?.trim()) return null
  return (
    <section className="rounded-2xl border border-gray-200 bg-gray-50 p-4">
      <h3 className="mb-2 font-semibold text-ink-600">简介</h3>
      <p className="text-sm leading-6 text-ink-100">{overview}</p>
    </section>
  )
}
