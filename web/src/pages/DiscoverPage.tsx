import { useEffect, useMemo, useState } from 'react'

import { discoverAPI, type DiscoverItem, type DiscoverSection } from '../api/discover'
import { AdultPerformerModal } from './AdultPerformerModal'
import { ContentRow, DiscoverSkeleton } from './DiscoverContentRow'
import { DiscoverDetailModal } from './DiscoverDetailModal'
import { DiscoverEmptySelection, DiscoverHeader, DiscoverResults } from './DiscoverPageSections'
import { DiscoverSectionPickerModal } from './DiscoverSectionPickerModal'
import {
  defaultSections,
  orderSelectedSections,
  readCachedDiscoverRows,
  writeCachedDiscoverRow,
} from './discoverPageModel'

export function DiscoverPage() {
  const [sections, setSections] = useState<DiscoverSection[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [rows, setRows] = useState<Record<string, DiscoverItem[]>>({})
  const [rowPages, setRowPages] = useState<Record<string, number>>({})
  const [rowCanNext, setRowCanNext] = useState<Record<string, boolean>>({})
  const [rowLoading, setRowLoading] = useState<Record<string, boolean>>({})
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({})
  const [sectionsReady, setSectionsReady] = useState(false)
  const [selectionSaving, setSelectionSaving] = useState(false)
  const [selectionError, setSelectionError] = useState('')
	const [sectionPickerOpen, setSectionPickerOpen] = useState(false)
	const [sectionPickerDraft, setSectionPickerDraft] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [activeItem, setActiveItem] = useState<DiscoverItem | null>(null)
  const [reloadSeq, setReloadSeq] = useState(0)
  const [imageVersion, setImageVersion] = useState<string>()
  const [refreshImageVersion, setRefreshImageVersion] = useState<string>()
	const [searchQuery, setSearchQuery] = useState('')
	const [searchItems, setSearchItems] = useState<DiscoverItem[]>([])
	const [searchLoading, setSearchLoading] = useState(false)
	const [searchErrors, setSearchErrors] = useState<Record<string, string>>({})
	const [searchDone, setSearchDone] = useState(false)

  useEffect(() => {
    let cancelled = false
    setSectionsReady(false)
    Promise.all([discoverAPI.sections(), discoverAPI.preference()])
      .then(async ([items, preference]) => {
        if (cancelled) return
        const available = new Set(items.map((item) => item.key))
        const fallback = defaultSections.filter((key) => available.has(key))
        const saved = preference.selected_sections.filter((key) => available.has(key))
        const nextSelected = orderSelectedSections(preference.configured ? saved : fallback, items)
        if (!preference.configured) {
          await discoverAPI.savePreference(nextSelected)
        }
        if (cancelled) return
        setSections(items)
        const cached = readCachedDiscoverRows(nextSelected)
        setSelected(nextSelected)
        setRowPages(Object.fromEntries(nextSelected.map((key) => [key, 1])))
        setRows(cached.rows)
        setRowCanNext(cached.rowCanNext)
        setSectionsReady(true)
      })
      .catch((error) => {
        if (cancelled) return
        setSections([])
        setSelected([])
        setSelectionError(discoverPreferenceErrorMessage(error))
        setSectionsReady(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (!sectionsReady) return
    const available = new Set(sections.map((section) => section.key))
    const activeSelected = orderSelectedSections(
      selected.filter((key) => available.has(key)),
      sections,
    )
    if (
      activeSelected.length !== selected.length ||
      activeSelected.some((key, index) => key !== selected[index])
    ) {
      setSelected(activeSelected)
      return
    }
    if (selected.length === 0) {
      setRows({})
      setRowLoading({})
      setRowCanNext({})
      setRowErrors({})
      setLoading(false)
      return
    }
    let cancelled = false
    setLoading(true)
    setRowErrors({})
    setRowLoading(Object.fromEntries(selected.map((key) => [key, true])))
    setRows((current) => {
      const next: Record<string, DiscoverItem[]> = {}
      for (const key of selected) {
        next[key] = current[key] ?? []
      }
      return next
    })
    let pending = selected.length
    const markDone = () => {
      pending -= 1
      if (!cancelled && pending <= 0) setLoading(false)
    }
    for (const key of selected) {
      const page = rowPages[key] ?? 1
      discoverAPI
        .feed([key], page)
        .then((feed) => {
          if (cancelled) return
          const error = feed.meta[key]?.error
          const nextItems = feed.items[key] ?? []
          const nextCanNext = Boolean(feed.meta[key]?.has_next)
          setRows((current) => {
            if (error && nextItems.length === 0 && (current[key]?.length ?? 0) > 0) {
              return current
            }
            return { ...current, [key]: nextItems }
          })
          setRowCanNext((current) => {
            if (error && nextItems.length === 0 && key in current) {
              return current
            }
            return { ...current, [key]: nextCanNext }
          })
          if (!error) {
            writeCachedDiscoverRow(key, page, nextItems, nextCanNext)
          }
          setRowErrors((current) => updateDiscoverRowError(current, key, error))
        })
        .catch((err) => {
          if (cancelled) return
          const message = discoverRequestErrorMessage(err)
          setRows((current) => ((current[key]?.length ?? 0) > 0 ? current : { ...current, [key]: [] }))
          setRowCanNext((current) => (key in current ? current : { ...current, [key]: false }))
          setRowErrors((current) => ({ ...current, [key]: message }))
        })
        .finally(() => {
          if (!cancelled) {
            setRowLoading((current) => ({ ...current, [key]: false }))
          }
          markDone()
        })
    }
    return () => {
      cancelled = true
    }
  }, [sections, sectionsReady, selected, rowPages, reloadSeq])

  const sectionMap = useMemo(
    () => new Map(sections.map((section) => [section.key, section])),
    [sections],
  )
  const hasContent = selected.some((key) => (rows[key] ?? []).length > 0)
  const sectionLabel = (key: string) => sectionMap.get(key)?.label ?? key
	const searchGroups = useMemo(() => groupDiscoverSearchItems(searchItems), [searchItems])
	const showSearchArea = sectionsReady && (
		searchLoading ||
		searchGroups.length > 0 ||
		Object.keys(searchErrors).length > 0 ||
		(searchDone && searchItems.length === 0)
	)

  const openSectionPicker = () => {
		setSectionPickerDraft(selected)
		setSelectionError('')
		setSectionPickerOpen(true)
	}

	const toggleSectionPickerDraft = (key: string) => {
		setSectionPickerDraft((current) => orderSelectedSections(
			current.includes(key) ? current.filter((item) => item !== key) : [...current, key],
			sections,
		))
	}

	const saveSectionSelection = async () => {
    if (selectionSaving) return
		const next = orderSelectedSections(sectionPickerDraft, sections)
		if (next.length === selected.length && next.every((key, index) => key === selected[index])) {
			setSectionPickerOpen(false)
			return
		}
    setSelectionSaving(true)
    setSelectionError('')
    try {
      const saved = await discoverAPI.savePreference(next)
		const savedSelection = orderSelectedSections(saved.selected_sections, sections)
      setSelected(savedSelection)
		setSectionPickerDraft(savedSelection)
		setRowPages((current) => Object.fromEntries(
			savedSelection.map((key) => [key, current[key] ?? 1]),
		))
		setSectionPickerOpen(false)
    } catch (error) {
      setSelectionError(discoverPreferenceErrorMessage(error))
    } finally {
      setSelectionSaving(false)
    }
  }

  const changeDiscoverPage = (key: string, delta: number) => {
    setRowPages((current) => {
      const nextPage = Math.max(1, (current[key] ?? 1) + delta)
      if (nextPage === (current[key] ?? 1)) return current
      return { ...current, [key]: nextPage }
    })
  }

  const refreshDiscover = () => {
    const nextImageVersion = String(Date.now())
    setImageVersion(nextImageVersion)
    setRefreshImageVersion(nextImageVersion)
    setReloadSeq((current) => current + 1)
  }

	const searchDiscoverCatalog = async () => {
		const query = searchQuery.trim()
		setSearchDone(true)
		setSearchItems([])
		setSearchErrors({})
		if ([...query].length < 1) {
			setSearchErrors({ request: '请输入搜索词' })
			return
		}
		setSearchLoading(true)
		try {
			const result = await discoverAPI.search(query)
			setSearchItems(result.items)
			setSearchErrors(result.errors)
		} catch (error) {
			const message = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
			setSearchErrors({ request: message || '聚合搜索失败' })
		} finally {
			setSearchLoading(false)
		}
	}

	const handleAdultFollowChanged = (followed: boolean) => {
		const source = activeItem?.source
		const sourceID = activeItem?.provider_id
		setSearchItems((current) => current.map((item) => (
			item.source === source && item.provider_id === sourceID ? { ...item, followed } : item
		)))
		setActiveItem((current) => current ? { ...current, followed } : current)
		setReloadSeq((current) => current + 1)
	}

  return (
    <div className="mx-auto w-full max-w-[1680px] space-y-10 px-4 py-6 md:px-6 md:py-8">
      <DiscoverHeader
        selectedCount={selected.length}
        sectionsReady={sectionsReady}
        loading={loading}
		selectionSaving={selectionSaving}
		searchQuery={searchQuery}
		searchLoading={searchLoading}
        onRefresh={refreshDiscover}
		onOpenSectionPicker={openSectionPicker}
		onSearchQueryChange={setSearchQuery}
		onSearch={() => void searchDiscoverCatalog()}
      />

		{sectionPickerOpen && (
			<DiscoverSectionPickerModal
				sections={sections}
				selected={sectionPickerDraft}
				saving={selectionSaving}
				error={selectionError}
				onToggle={toggleSectionPickerDraft}
				onClose={() => setSectionPickerOpen(false)}
				onSave={() => void saveSectionSelection()}
			/>
		)}

      {!sectionsReady && <div className="xl:ml-[252px]"><DiscoverSkeleton /></div>}

      {selectionError && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
          {selectionError}
        </div>
      )}

		{showSearchArea && <div className="space-y-8 xl:ml-[252px]">
			{sectionsReady && Object.keys(searchErrors).length > 0 && (
				<div className="space-y-1 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
					<p className="font-semibold">以下搜索源未返回结果：</p>
					{Object.entries(searchErrors).map(([source, message]) => <p key={source}>{message}</p>)}
				</div>
			)}

			{sectionsReady && searchLoading && (
				<div className="rounded-lg border border-primary-100 bg-primary-50 px-4 py-3 text-sm text-brand-500">
					正在聚合搜索 TMDb、豆瓣、Bangumi{sections.some((section) => section.group === 'adult') ? ' 与 JavDB' : ''}…
				</div>
			)}

			{sectionsReady && searchGroups.map((group, index) => (
				<ContentRow
					key={group.key}
					title={`搜索结果 · ${group.label}`}
					items={group.items}
					priority={index === 0}
					onSelect={setActiveItem}
				/>
			))}

			{sectionsReady && searchDone && !searchLoading && Object.keys(searchErrors).length === 0 && searchItems.length === 0 && (
				<div className="rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm text-gray-500">
					没有找到匹配的电影、剧集、动漫、女优或成人作品
				</div>
			)}
		</div>}

      {sectionsReady && !loading && selected.length === 0 && !searchDone && (
		<div className="xl:ml-[252px]"><DiscoverEmptySelection /></div>
	  )}

      {sectionsReady && selected.length > 0 && (
        <DiscoverResults
          selected={selected}
          rows={rows}
          rowLoading={rowLoading}
          rowErrors={rowErrors}
          rowPages={rowPages}
          rowCanNext={rowCanNext}
          loading={loading}
          hasContent={hasContent}
          imageVersion={imageVersion}
          refreshImageVersion={refreshImageVersion}
          sectionLabel={sectionLabel}
          onPageChange={changeDiscoverPage}
          onSelect={setActiveItem}
        />
      )}

		{activeItem?.media_type === 'person' && (
			<AdultPerformerModal
				item={activeItem}
				onClose={() => setActiveItem(null)}
				onSelectWork={setActiveItem}
				onFollowChanged={handleAdultFollowChanged}
			/>
		)}

		{activeItem && activeItem.media_type !== 'person' && (
        <DiscoverDetailModal
          item={activeItem}
          onClose={() => setActiveItem(null)}
          onSelectPerformer={setActiveItem}
        />
      )}
    </div>
  )
}

function updateDiscoverRowError(
  current: Record<string, string>,
  key: string,
  error?: string,
): Record<string, string> {
  if (error) return { ...current, [key]: error }
  if (!(key in current)) return current
  const next = { ...current }
  delete next[key]
  return next
}

function discoverRequestErrorMessage(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err)
  const lower = raw.toLowerCase()
  if (lower.includes('timeout') || lower.includes('deadline')) {
    return '推荐源请求超时，已跳过本次加载'
  }
  if (lower.includes('network')) {
    return '推荐源网络不可用，已跳过本次加载'
  }
  return '推荐源暂时不可用，已跳过本次加载'
}

function groupDiscoverSearchItems(items: DiscoverItem[]): Array<{
	key: string
	label: string
	items: DiscoverItem[]
}> {
	const definitions = [
		{ key: 'movie', label: '电影' },
		{ key: 'tv', label: '剧集' },
		{ key: 'anime', label: '动漫' },
		{ key: 'person', label: '女优' },
		{ key: 'adult', label: '成人作品' },
	]
	const grouped = new Map<string, DiscoverItem[]>()
	for (const item of items) {
		const mediaType = item.media_type?.trim().toLowerCase() || 'other'
		grouped.set(mediaType, [...(grouped.get(mediaType) ?? []), item])
	}
	const groups = definitions
		.map((definition) => ({ ...definition, items: grouped.get(definition.key) ?? [] }))
		.filter((group) => group.items.length > 0)
	const known = new Set(definitions.map((definition) => definition.key))
	const otherItems = items.filter((item) => !known.has(item.media_type?.trim().toLowerCase() || 'other'))
	if (otherItems.length > 0) groups.push({ key: 'other', label: '其他', items: otherItems })
	return groups
}

function discoverPreferenceErrorMessage(error: unknown): string {
  const message = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
  return message ? `发现模块设置保存失败：${message}` : '发现模块设置无法从数据库读取或保存，请稍后重试'
}
