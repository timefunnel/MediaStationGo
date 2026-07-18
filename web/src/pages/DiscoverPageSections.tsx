import { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, ArrowLeft, Layers3, List, LoaderCircle, RefreshCw, Search, Sparkles } from 'lucide-react'

import type { DiscoverItem } from '../api/discover'
import { ContentRow } from './DiscoverContentRow'

type SectionLabel = (key: string) => string

export function DiscoverHeader({
  selectedCount,
  sectionsReady,
  loading,
	selectionSaving,
	searchQuery,
	searchLoading,
  searchActive,
  onRefresh,
	onOpenSectionPicker,
	onSearchQueryChange,
	onSearch,
  onClearSearch,
}: {
  selectedCount: number
  sectionsReady: boolean
  loading: boolean
	selectionSaving: boolean
  searchQuery: string
  searchLoading: boolean
  searchActive: boolean
  onRefresh: () => void
	onOpenSectionPicker: () => void
	onSearchQueryChange: (value: string) => void
	onSearch: () => void
  onClearSearch: () => void
}) {
  return (
	<header className="grid items-start gap-6 border-b border-gray-200/80 pb-6 xl:grid-cols-[220px_minmax(0,1fr)] xl:gap-8">
		<div className="min-w-0">
			<div className="flex items-center gap-3">
				<Sparkles className="h-6 w-6 shrink-0 text-brand-500" />
				<h1 className="font-display text-4xl font-bold tracking-tight text-ink-600">发现</h1>
				<button
					type="button"
					onClick={onOpenSectionPicker}
					disabled={!sectionsReady || selectionSaving}
					aria-label="选择发现模块"
					className="inline-flex h-9 items-center gap-1.5 rounded-xl border border-gray-200 bg-white px-2.5 text-xs font-semibold text-ink-600 transition hover:border-primary-300 hover:text-brand-500 disabled:cursor-not-allowed disabled:opacity-50"
				>
					<Layers3 size={14} />
					模块
					<span className="rounded-full bg-gray-100 px-1.5 py-0.5 text-[10px] text-sand-500">{selectedCount}</span>
				</button>
			</div>
			<p className="mt-2 text-sm leading-6 text-ink-50">多源内容推荐，可按需组合显示</p>
		</div>
		<form
			onSubmit={(event) => {
				event.preventDefault()
				onSearch()
			}}
			className="flex w-full flex-col gap-2 sm:flex-row sm:items-center"
		>
			<input
				type="search"
				value={searchQuery}
				onChange={(event) => onSearchQueryChange(event.target.value)}
				placeholder="搜索电影、剧集、动漫、女优或番号"
				aria-label="聚合搜索"
				className="h-11 min-w-0 flex-1 rounded-xl border border-gray-200 bg-white px-4 text-sm text-ink-600 outline-none transition focus:border-primary-400 focus:ring-2 focus:ring-primary-100"
			/>
			<button
				type="submit"
				disabled={!sectionsReady || searchLoading || searchQuery.trim().length < 1}
				className="inline-flex h-11 shrink-0 items-center justify-center gap-1.5 rounded-xl border border-primary-300 bg-primary-500/10 px-4 text-xs font-semibold text-brand-500 transition hover:bg-primary-500/15 disabled:cursor-not-allowed disabled:opacity-50"
			>
				{searchLoading ? <LoaderCircle size={14} className="animate-spin" /> : <Search size={14} />}
				聚合搜索
			</button>
			{searchActive ? (
				<button
					type="button"
					onClick={onClearSearch}
					className="inline-flex h-11 shrink-0 items-center justify-center gap-1.5 rounded-xl border border-gray-200 bg-white px-4 text-xs font-semibold text-ink-600 transition hover:border-primary-300 hover:text-brand-500"
				>
					<ArrowLeft size={14} />
					返回发现
				</button>
			) : (
				<button
					type="button"
					onClick={onRefresh}
					disabled={!sectionsReady || selectionSaving || selectedCount === 0}
					className="inline-flex h-11 shrink-0 items-center justify-center gap-1.5 rounded-xl border border-gray-200 bg-white px-4 text-xs font-semibold text-ink-600 transition hover:border-primary-300 hover:text-brand-500 disabled:cursor-not-allowed disabled:opacity-50"
				>
					<RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
					刷新
				</button>
			)}
		</form>
	</header>
  )
}

export function DiscoverEmptySelection() {
  return (
    <div className="rounded-2xl border border-gray-200 bg-white p-10 text-center text-sand-500">
      至少选择一个推荐源，小宇宙才会开始转动。
    </div>
  )
}

export function DiscoverResults({
  selected,
  rows,
  rowLoading,
  rowErrors,
  rowPages,
  rowCanNext,
  loading,
  hasContent,
  imageVersion,
  refreshImageVersion,
  sectionLabel,
  onPageChange,
  onSelect,
}: {
  selected: string[]
  rows: Record<string, DiscoverItem[]>
  rowLoading: Record<string, boolean>
  rowErrors: Record<string, string>
  rowPages: Record<string, number>
  rowCanNext: Record<string, boolean>
  loading: boolean
  hasContent: boolean
  imageVersion?: string
  refreshImageVersion?: string
  sectionLabel: SectionLabel
  onPageChange: (key: string, delta: number) => void
  onSelect: (item: DiscoverItem) => void
}) {
  const sectionTopOffset = 96
  const hasRowErrors = Object.keys(rowErrors).length > 0
  const rowRefs = useRef<Record<string, HTMLDivElement | null>>({})
  const navigableKeys = useMemo(
    () => selected.filter((key) => rowLoading[key] || (rows[key]?.length ?? 0) > 0),
    [rowLoading, rows, selected],
  )
  const [activeKey, setActiveKey] = useState('')

  useEffect(() => {
    if (navigableKeys.length === 0) {
      setActiveKey('')
      return
    }

    const firstRow = navigableKeys.map((key) => rowRefs.current[key]).find(Boolean)
    const scrollContainer = firstRow?.closest('main')
    if (!scrollContainer) return

    let frame = 0
    const updateActiveKey = () => {
      const activationLine = scrollContainer.getBoundingClientRect().top + sectionTopOffset + 1
      let nextKey = navigableKeys[0]
      for (const key of navigableKeys) {
        const row = rowRefs.current[key]
        if (!row) continue
        if (row.getBoundingClientRect().top <= activationLine) {
          nextKey = key
          continue
        }
        break
      }
      setActiveKey((current) => (current === nextKey ? current : nextKey))
    }
    const scheduleUpdate = () => {
      if (frame) return
      frame = window.requestAnimationFrame(() => {
        frame = 0
        updateActiveKey()
      })
    }

    updateActiveKey()
    scrollContainer.addEventListener('scroll', scheduleUpdate, { passive: true })
    window.addEventListener('resize', scheduleUpdate)
    return () => {
      scrollContainer.removeEventListener('scroll', scheduleUpdate)
      window.removeEventListener('resize', scheduleUpdate)
      if (frame) window.cancelAnimationFrame(frame)
    }
  }, [navigableKeys])

  const jumpToSection = (key: string) => {
    const row = rowRefs.current[key]
    if (!row) return
    const scrollContainer = row.closest('main')
    if (!scrollContainer) return
    const targetTop = scrollContainer.scrollTop
      + row.getBoundingClientRect().top
      - scrollContainer.getBoundingClientRect().top
      - sectionTopOffset
    setActiveKey(key)
    scrollContainer.scrollTop = Math.max(0, Math.round(targetTop))
  }

  return (
    <div className={navigableKeys.length > 1 ? 'xl:grid xl:grid-cols-[220px_minmax(0,1fr)] xl:gap-8' : ''}>
      {navigableKeys.length > 1 && (
        <DiscoverSectionRail
          keys={navigableKeys}
          activeKey={activeKey}
          sectionLabel={sectionLabel}
          onSelect={jumpToSection}
        />
      )}

      <div className="min-w-0 space-y-10">
        {selected.map((key, rowIndex) => {
          const items = rows[key] ?? []
          if (items.length === 0) {
            if (rowLoading[key]) {
              return (
                <div
                  key={key}
                  ref={(element) => { rowRefs.current[key] = element }}
                  id={discoverSectionID(key)}
                  className="scroll-mt-24"
                >
                  <DiscoverRowSkeleton title={sectionLabel(key)} />
                </div>
              )
            }
            return null
          }
          return (
            <div
              key={key}
              ref={(element) => { rowRefs.current[key] = element }}
              id={discoverSectionID(key)}
              className="scroll-mt-24"
            >
              <ContentRow
                title={sectionLabel(key)}
                items={items}
                page={rowPages[key] ?? 1}
                canNext={Boolean(rowCanNext[key])}
                imageVersion={imageVersion}
                refreshImageVersion={refreshImageVersion}
                priority={rowIndex === 0}
                onPageChange={(delta) => onPageChange(key, delta)}
                onSelect={onSelect}
              />
            </div>
          )
        })}

        {hasRowErrors && (
          <DiscoverRowErrors rowErrors={rowErrors} sectionLabel={sectionLabel} />
        )}

        {!loading && !hasContent && !hasRowErrors && <DiscoverNoContent />}
      </div>
    </div>
  )
}

function DiscoverSectionRail({
  keys,
  activeKey,
  sectionLabel,
  onSelect,
}: {
  keys: string[]
  activeKey: string
  sectionLabel: SectionLabel
  onSelect: (key: string) => void
}) {
  return (
    <aside className="hidden xl:block">
      <nav
        aria-label="发现模块快速跳转"
        className="sticky top-24 rounded-2xl border border-gray-200 bg-white/95 p-4 shadow-sm backdrop-blur"
      >
        <div className="flex items-center gap-2 text-xs font-semibold text-sand-500">
          <List size={14} />
          快速跳转
        </div>
        <div className="mt-4 border-l border-gray-200 pl-0.5">
          {keys.map((key) => {
            const active = key === activeKey
            return (
              <button
                key={key}
                type="button"
                aria-current={active ? 'true' : undefined}
                onClick={() => onSelect(key)}
                className={
                  'group -ml-[7px] flex w-[calc(100%+7px)] items-center gap-2 py-2 text-left text-xs font-semibold transition ' +
                  (active ? 'text-brand-500' : 'text-gray-500 hover:text-ink-600')
                }
              >
                <span
                  className={
                    'h-3 w-3 flex-none rounded-full border-2 bg-white transition ' +
                    (active
                      ? 'border-primary-500 ring-4 ring-primary-500/10'
                      : 'border-gray-300 group-hover:border-primary-300')
                  }
                />
                <span className="line-clamp-2">{sectionLabel(key)}</span>
              </button>
            )
          })}
        </div>
      </nav>
    </aside>
  )
}

function discoverSectionID(key: string): string {
  return `discover-section-${key.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function DiscoverRowErrors({
  rowErrors,
  sectionLabel,
}: {
  rowErrors: Record<string, string>
  sectionLabel: SectionLabel
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border border-amber-300/70 bg-amber-50 px-3 py-2 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-100">
      <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0 text-amber-500" />
      <div className="space-y-1 text-xs">
        <p className="font-semibold">部分推荐源暂不可用，其他已加载内容不受影响。</p>
        {Object.entries(rowErrors).map(([key, message]) => (
          <p key={key}>{sectionLabel(key)}：{message}</p>
        ))}
      </div>
    </div>
  )
}

function DiscoverNoContent() {
  return (
    <div className="rounded-2xl border border-gray-200 bg-white p-10 text-center">
      <p className="text-sand-500">
        当前选择的推荐源暂未返回内容，可切换豆瓣 / Bangumi 或检查网络代理。
      </p>
    </div>
  )
}

function DiscoverRowSkeleton({ title }: { title: string }) {
  return (
    <section className="space-y-4">
      <h2 className="pl-1 font-display text-2xl font-semibold text-ink-600">{title}</h2>
      <div className="grid grid-cols-2 gap-5 sm:grid-cols-3 md:grid-cols-[repeat(auto-fill,minmax(190px,1fr))]">
        {Array.from({ length: 24 }, (_, index) => index).map((item) => (
          <div key={item} className="aspect-[2/3] animate-pulse rounded-xl bg-gray-100" />
        ))}
      </div>
    </section>
  )
}
