import { Clapperboard, Tags, UserRound, X } from 'lucide-react'

import type { ReactNode } from 'react'
import type { ActorFacet } from './libraryActorFilterModel'
import type { CategoryFacet } from './libraryCategoryFilterModel'
import type { AdultTypeFacet } from './libraryAdultTypeFilterModel'

export function LibraryFilterBar({
  categories,
  selectedCategory,
  onCategoryChange,
  adultTypes,
  selectedAdultType,
  onAdultTypeChange,
  actors,
  selectedActor,
  onActorChange,
}: {
  categories: CategoryFacet[]
  selectedCategory: string
  onCategoryChange: (category: string) => void
  adultTypes: AdultTypeFacet[]
  selectedAdultType: string
  onAdultTypeChange: (adultType: string) => void
  actors: ActorFacet[]
  selectedActor: string
  onActorChange: (actor: string) => void
}) {
  if (categories.length === 0 && adultTypes.length === 0 && actors.length === 0) return null

  return (
    <div className="grid gap-2 border-y border-gray-200 py-3 sm:grid-cols-2 lg:grid-cols-3">
      {categories.length > 0 && (
        <FilterSelect
          icon={<Tags size={18} />}
          label="按自动分类筛选"
          emptyLabel="全部分类"
          value={selectedCategory}
          options={categories}
          onChange={onCategoryChange}
        />
      )}
      {adultTypes.length > 0 && (
        <FilterSelect
          icon={<Clapperboard size={18} />}
          label="按成人类型筛选"
          emptyLabel="全部成人类型"
          value={selectedAdultType}
          options={adultTypes}
          onChange={onAdultTypeChange}
        />
      )}
      {actors.length > 0 && (
        <FilterSelect
          icon={<UserRound size={18} />}
          label="按演员筛选"
          emptyLabel="全部演员"
          value={selectedActor}
          options={actors}
          onChange={onActorChange}
        />
      )}
    </div>
  )
}

function FilterSelect({
  icon,
  label,
  emptyLabel,
  value,
  options,
  onChange,
}: {
  icon: ReactNode
  label: string
  emptyLabel: string
  value: string
  options: Array<{ name: string; count: number }>
  onChange: (value: string) => void
}) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span className="inline-flex h-9 w-9 shrink-0 items-center justify-center text-brand-600" title={label}>
        {icon}
      </span>
      <label className="min-w-0 flex-1">
        <span className="sr-only">{label}</span>
        <select
          className="input-base h-11 w-full py-2 text-base leading-6 sm:text-sm"
          value={value}
          onChange={(event) => onChange(event.target.value)}
        >
          <option value="">{emptyLabel}</option>
          {options.map((option) => (
            <option key={option.name} value={option.name}>
              {option.name} ({option.count})
            </option>
          ))}
        </select>
      </label>
      {value && (
        <button
          type="button"
          className="btn-ghost h-10 w-10 shrink-0 justify-center p-0"
          title={`清除${label.replace('按', '')}`}
          aria-label={`清除${label.replace('按', '')}`}
          onClick={() => onChange('')}
        >
          <X size={17} />
        </button>
      )}
    </div>
  )
}
