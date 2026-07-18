import { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, Flame, List, LoaderCircle, RefreshCw, Search, Sparkles } from 'lucide-react'

import type { DiscoverItem, DiscoverSection } from '../api/discover'
import { ContentRow } from './DiscoverContentRow'

type SectionLabel = (key: string) => string

export function DiscoverHeader({
  sections,
  selected,
  sectionsReady,
  loading,
	selectionSaving,
	adultSearchQuery,
	adultSearchLoading,
  onRefresh,
  onToggleSection,
	onAdultSearchQueryChange,
	onAdultSearch,
}: {
  sections: DiscoverSection[]
  selected: string[]
  sectionsReady: boolean
  loading: boolean
	selectionSaving: boolean
	adultSearchQuery: string
	adultSearchLoading: boolean
  onRefresh: () => void
  onToggleSection: (key: string) => void
	onAdultSearchQueryChange: (value: string) => void
	onAdultSearch: () => void
}) {
	const generalSections = sections.filter((section) => section.group !== 'adult')
	const adultSections = sections.filter((section) => section.group === 'adult')

  return (
    <header className="flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
      <div className="flex items-center gap-4">
        <div className="rounded-2xl border border-primary-500/20 bg-gradient-to-br from-primary-500/20 to-primary-600/10 p-3">
          <Sparkles className="h-8 w-8 text-brand-500" />
        </div>
        <div>
          <h1 className="font-display text-4xl font-bold tracking-tight text-ink-600">
            发现
          </h1>
          <p className="mt-1 text-base text-ink-50">
            多源推荐：TMDb / 豆瓣 / Bangumi，可按需组合显示
          </p>
        </div>
      </div>

      <div className="flex flex-col gap-3 lg:items-end">
        <button
          type="button"
          onClick={onRefresh}
          disabled={!sectionsReady || selectionSaving || selected.length === 0}
          className="inline-flex items-center justify-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs font-semibold text-ink-600 transition hover:border-primary-300 hover:text-brand-500 disabled:cursor-not-allowed disabled:opacity-50"
        >
          <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
          刷新
        </button>
		<div className="space-y-2 lg:max-w-3xl">
			<div className="flex flex-wrap justify-start gap-2 lg:justify-end">
				{generalSections.map((section) => (
					<DiscoverSectionToggle
						key={section.key}
						section={section}
						active={selected.includes(section.key)}
						onToggle={onToggleSection}
						disabled={selectionSaving}
					/>
				))}
			</div>
			{adultSections.length > 0 && (
				<div className="space-y-2 border-t border-rose-200/70 pt-2">
					<div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-end">
						<div className="inline-flex items-center gap-1.5 text-xs font-semibold text-rose-600">
							<Flame size={14} />
							成人专区
						</div>
						<div className="flex flex-wrap gap-2">
							{adultSections.map((section) => (
								<DiscoverSectionToggle
									key={section.key}
									section={section}
									active={selected.includes(section.key)}
									onToggle={onToggleSection}
									disabled={selectionSaving}
									adult
								/>
							))}
						</div>
					</div>
					<form
						onSubmit={(event) => {
							event.preventDefault()
							onAdultSearch()
						}}
						className="flex justify-end gap-2"
					>
						<input
							type="search"
							value={adultSearchQuery}
							onChange={(event) => onAdultSearchQueryChange(event.target.value)}
							placeholder="搜索任意 JavDB 女优"
							aria-label="搜索女优"
							className="h-9 min-w-0 flex-1 rounded-lg border border-rose-200 bg-white px-3 text-sm text-ink-600 outline-none transition focus:border-rose-400 focus:ring-2 focus:ring-rose-100 sm:max-w-xs"
						/>
						<button
							type="submit"
							disabled={adultSearchLoading || adultSearchQuery.trim().length < 2}
							className="inline-flex h-9 items-center gap-1.5 rounded-lg border border-rose-300 bg-rose-50 px-3 text-xs font-semibold text-rose-700 transition hover:bg-rose-100 disabled:cursor-not-allowed disabled:opacity-50"
						>
							{adultSearchLoading ? <LoaderCircle size={14} className="animate-spin" /> : <Search size={14} />}
							搜索女优
						</button>
					</form>
				</div>
			)}
		</div>
      </div>
    </header>
  )
}

function DiscoverSectionToggle({
	section,
	active,
	onToggle,
	adult = false,
	disabled = false,
}: {
	section: DiscoverSection
	active: boolean
	onToggle: (key: string) => void
	adult?: boolean
	disabled?: boolean
}) {
	return (
		<button
			type="button"
			disabled={disabled}
			onClick={() => onToggle(section.key)}
			className={
				'rounded-full border px-3 py-1.5 text-xs font-semibold transition ' +
				(active
					? adult
						? 'border-rose-400 bg-rose-50 text-rose-700'
						: 'border-primary-400 bg-primary-400/15 text-brand-500'
					: adult
						? 'border-rose-200 bg-white text-rose-500 hover:border-rose-400 hover:text-rose-700'
						: 'border-gray-200 bg-white text-gray-500 hover:border-primary-300 hover:text-ink-600') +
				(disabled ? ' cursor-not-allowed opacity-50' : '')
			}
		>
			{section.label}
		</button>
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
  const hasRowErrors = Object.keys(rowErrors).length > 0
  const rowRefs = useRef<Record<string, HTMLDivElement | null>>({})
  const navigableKeys = useMemo(
    () => selected.filter((key) => rowLoading[key] || (rows[key]?.length ?? 0) > 0),
    [rowLoading, rows, selected],
  )
  const navigableKeySignature = navigableKeys.join('\u0000')
  const [activeKey, setActiveKey] = useState('')

  useEffect(() => {
    if (navigableKeys.length === 0) {
      setActiveKey('')
      return
    }

    let frame = 0
    const updateActiveKey = () => {
      const activationLine = 128
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
    window.addEventListener('scroll', scheduleUpdate, { passive: true })
    window.addEventListener('resize', scheduleUpdate)
    return () => {
      window.removeEventListener('scroll', scheduleUpdate)
      window.removeEventListener('resize', scheduleUpdate)
      if (frame) window.cancelAnimationFrame(frame)
    }
  }, [navigableKeySignature])

  const jumpToSection = (key: string) => {
    const row = rowRefs.current[key]
    if (!row) return
    setActiveKey(key)
    row.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  return (
    <div className={navigableKeys.length > 1 ? 'xl:grid xl:grid-cols-[180px_minmax(0,1fr)] xl:gap-7' : ''}>
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
                  className="scroll-mt-28"
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
              className="scroll-mt-28"
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
      <div className="grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8">
        {Array.from({ length: 24 }, (_, index) => index).map((item) => (
          <div key={item} className="aspect-[2/3] animate-pulse rounded-xl bg-gray-100" />
        ))}
      </div>
    </section>
  )
}
