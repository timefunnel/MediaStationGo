import { useEffect, useMemo, useState } from 'react'
import { Heart, LoaderCircle, UserRound, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { imageURL } from '../api/client'
import { discoverAPI, type AdultPerformerFollow, type DiscoverItem } from '../api/discover'
import { ContentRow } from './DiscoverContentRow'

export function AdultPerformerModal({
	item,
	onClose,
	onSelectWork,
	onFollowChanged,
}: {
	item: DiscoverItem
	onClose: () => void
	onSelectWork: (item: DiscoverItem) => void
	onFollowChanged: (followed: boolean) => void
}) {
	const source = item.source || 'javdb'
	const sourceID = item.provider_id || ''
	const [resolvedItem, setResolvedItem] = useState(item)
	const [follows, setFollows] = useState<AdultPerformerFollow[]>([])
	const [works, setWorks] = useState<DiscoverItem[]>([])
	const [page, setPage] = useState(1)
	const [canNext, setCanNext] = useState(false)
	const [loading, setLoading] = useState(true)
	const [saving, setSaving] = useState(false)
	const [error, setError] = useState('')
	const [profileError, setProfileError] = useState('')
	const follow = useMemo(
		() => follows.find((entry) => entry.source === source && entry.source_id === sourceID),
		[follows, source, sourceID],
	)
	const portraitSource = adultPerformerPortraitSource(resolvedItem.poster_url, follow?.image_url)
	const portrait = imageURL(portraitSource, undefined, { maxWidth: 240, quality: 86, retryFailed: true })

	useEffect(() => {
		setResolvedItem(item)
		setProfileError('')
		setPage(1)
	}, [item.title, source, sourceID])

	useEffect(() => {
		let cancelled = false
		if (!sourceID) {
			setLoading(false)
			setError('演员来源信息不完整')
			return
		}
		setLoading(true)
		setError('')
		Promise.all([
			discoverAPI.adultFollows(),
			discoverAPI.adultPerformerWorks(source, sourceID, page, page === 1 ? item.title : undefined),
		])
			.then(([nextFollows, result]) => {
				if (cancelled) return
				setFollows(nextFollows)
				setWorks(result.items ?? [])
				setCanNext(Boolean(result.has_next))
				if (page === 1) {
					setProfileError(result.performer_error ?? '')
					if (result.performer) {
						setResolvedItem((current) => ({
							...current,
							...result.performer,
							title: result.performer?.title?.trim() || current.title,
							poster_url: result.performer?.poster_url?.trim() || current.poster_url,
						}))
					}
				}
			})
			.catch((requestError) => {
				if (cancelled) return
				const message = (requestError as { response?: { data?: { error?: string } } })?.response?.data?.error
				setError(message || '演员作品暂时无法加载')
			})
			.finally(() => {
				if (!cancelled) setLoading(false)
			})
		return () => {
			cancelled = true
		}
	}, [item.title, page, source, sourceID])

	const toggleFollow = async () => {
		if (!sourceID || saving) return
		setSaving(true)
		try {
			let followed = false
			if (follow) {
				await discoverAPI.unfollowAdultPerformer(follow.id)
				setFollows((current) => current.filter((entry) => entry.id !== follow.id))
				toast.success('已取消关注')
			} else {
				const created = await discoverAPI.followAdultPerformer({
					name: resolvedItem.title,
					source,
					source_id: sourceID,
					image_url: portraitSource,
				})
				setFollows((current) => [...current, created])
				followed = true
				toast.success('已关注该女优')
			}
			onFollowChanged(followed)
		} catch (requestError) {
			const message = (requestError as { response?: { data?: { error?: string } } })?.response?.data?.error
			toast.error(message || '关注操作失败')
		} finally {
			setSaving(false)
		}
	}

	return (
		<div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-3 backdrop-blur-sm sm:p-5">
			<div className="max-h-[94vh] w-full max-w-7xl overflow-y-auto rounded-xl border border-white/60 bg-white p-4 shadow-2xl sm:p-5">
				<header className="mb-5 flex items-start justify-between gap-4">
					<div>
						<p className="text-xs font-semibold uppercase text-rose-500">JavDB 女优</p>
						<h2 className="mt-1 text-2xl font-bold text-ink-600">{resolvedItem.title}</h2>
					</div>
					<button
						type="button"
						onClick={onClose}
						aria-label="关闭"
						className="inline-flex h-9 w-9 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:border-gray-300 hover:text-ink-600"
					>
						<X size={18} />
					</button>
				</header>

				<section className="mb-6 flex flex-col gap-4 rounded-xl border border-gray-200 bg-gray-50 p-3 sm:flex-row sm:items-center sm:p-4">
					<div className="aspect-[3/4] w-28 shrink-0 overflow-hidden rounded-lg bg-gray-100 sm:w-32">
							{portrait ? (
								<img src={portrait} alt={resolvedItem.title} className="h-full w-full object-cover" />
							) : (
								<div className="flex h-full flex-col items-center justify-center gap-2 bg-gradient-to-br from-rose-50 to-rose-100 px-4 text-center text-rose-500">
									<UserRound size={38} strokeWidth={1.5} />
									<span className="text-xs font-semibold">JavDB 暂无头像</span>
								</div>
							)}
					</div>
					<div className="min-w-0 flex-1 space-y-2">
						<p className="text-sm font-semibold text-ink-600">{resolvedItem.title}</p>
						<p className="text-xs text-ink-50">选择下方作品可查看完整封面、发行日期与资源详情。</p>
						{profileError && (
							<p className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
								{profileError}
							</p>
						)}
					</div>
					<button
							type="button"
							disabled={!sourceID || saving}
							onClick={() => void toggleFollow()}
							className={
								'inline-flex h-10 w-full shrink-0 items-center justify-center gap-2 rounded-lg border px-4 text-sm font-semibold transition disabled:opacity-50 sm:w-auto ' +
								(follow
									? 'border-rose-300 bg-rose-50 text-rose-700'
									: 'border-gray-200 bg-white text-ink-600 hover:border-rose-300 hover:text-rose-600')
							}
						>
							{saving ? <LoaderCircle size={16} className="animate-spin" /> : <Heart size={16} fill={follow ? 'currentColor' : 'none'} />}
							{follow ? '已关注' : '关注女优'}
						</button>
				</section>

				<div className="min-w-0">
						{loading ? (
							<div className="flex min-h-52 items-center justify-center gap-2 text-sm text-gray-500">
								<LoaderCircle size={18} className="animate-spin" />
								正在加载近期作品
							</div>
						) : error ? (
							<div className="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800">{error}</div>
						) : works.length === 0 ? (
							<div className="rounded-lg border border-gray-200 bg-gray-50 p-4 text-sm text-gray-500">暂无可展示作品</div>
						) : (
							<ContentRow
								title="近期作品"
								items={works}
								page={page}
								canNext={canNext}
								priority
								cardSize="large"
								onPageChange={(delta) => setPage((current) => Math.max(1, current + delta))}
								onSelect={onSelectWork}
							/>
						)}
				</div>
			</div>
		</div>
	)
}

function adultPerformerPortraitSource(...values: Array<string | undefined>): string {
	for (const value of values) {
		const normalized = value?.trim() ?? ''
		if (!normalized || /actor[_-]unknow/i.test(normalized)) continue
		return normalized
	}
	return ''
}
