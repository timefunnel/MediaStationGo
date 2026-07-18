import { UserRound, X } from 'lucide-react'

import type { ActorFacet } from './libraryActorFilterModel'

export function LibraryActorFilter({
  actors,
  selected,
  onChange,
}: {
  actors: ActorFacet[]
  selected: string
  onChange: (actor: string) => void
}) {
  if (actors.length === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-2 border-y border-gray-200 py-3">
      <span className="inline-flex h-9 w-9 shrink-0 items-center justify-center text-brand-600" title="按演员筛选">
        <UserRound size={18} />
      </span>
      <label className="min-w-0 flex-1 sm:max-w-sm">
        <span className="sr-only">按演员筛选</span>
        <select
          className="input-base h-11 w-full py-2 text-base leading-6 sm:text-sm"
          value={selected}
          onChange={(event) => onChange(event.target.value)}
        >
          <option value="">全部演员</option>
          {actors.map((actor) => (
            <option key={actor.name} value={actor.name}>
              {actor.name} ({actor.count})
            </option>
          ))}
        </select>
      </label>
      {selected && (
        <button
          type="button"
          className="btn-ghost h-10 w-10 shrink-0 justify-center p-0"
          title="清除演员筛选"
          aria-label="清除演员筛选"
          onClick={() => onChange('')}
        >
          <X size={17} />
        </button>
      )}
    </div>
  )
}
