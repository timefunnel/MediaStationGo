import { useState } from 'react'
import { LibraryBig, Save, X } from 'lucide-react'

import type { Library, User } from '../types'

type AdminUserLibraryAccessModalProps = {
  user: User
  libraries: Library[]
  saving: boolean
  onClose: () => void
  onSave: (allowedLibraryIDs: string[]) => void
}

export function AdminUserLibraryAccessModal({
  user,
  libraries,
  saving,
  onClose,
  onSave,
}: AdminUserLibraryAccessModalProps) {
  const availableIDs = new Set(libraries.map((library) => library.id))
  const configuredIDs = user.allowed_library_ids ?? []
  const initialIDs = configuredIDs.filter((id) => availableIDs.has(id))
  const unavailableIDs = configuredIDs.filter((id) => !availableIDs.has(id))
  const [allowAll, setAllowAll] = useState(configuredIDs.length === 0)
  const [selectedIDs, setSelectedIDs] = useState<string[]>(initialIDs)

  const toggleAll = (checked: boolean) => {
    setAllowAll(checked)
    if (!checked && selectedIDs.length === 0) {
      setSelectedIDs(libraries.map((library) => library.id))
    }
  }

  const toggleLibrary = (id: string) => {
    setSelectedIDs((current) =>
      current.includes(id) ? current.filter((item) => item !== id) : [...current, id],
    )
  }

  const save = () => {
    if (allowAll) {
      onSave([])
      return
    }
    onSave(selectedIDs)
  }

  const canSave = allowAll || selectedIDs.length > 0

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-xl overflow-hidden rounded-lg border border-gray-200 bg-white shadow-2xl">
        <div className="flex items-center justify-between border-b border-gray-200 px-4 py-3">
          <div className="flex min-w-0 items-center gap-2">
            <LibraryBig size={18} className="shrink-0 text-brand-500" />
            <div className="min-w-0">
              <h2 className="truncate text-base font-semibold text-ink-600">分配媒体库</h2>
              <p className="truncate text-xs text-ink-50">{user.username}</p>
            </div>
          </div>
          <button type="button" className="icon-button" title="关闭" onClick={onClose} disabled={saving}>
            <X size={16} />
          </button>
        </div>

        <div className="max-h-[65vh] space-y-4 overflow-y-auto p-4">
          <label className="flex items-center justify-between gap-4 border-b border-gray-200 pb-3">
            <span>
              <span className="block text-sm font-medium text-ink-600">全部媒体库</span>
              <span className="block text-xs text-ink-50">以后新建的媒体库也会自动开放</span>
            </span>
            <input
              type="checkbox"
              className="h-4 w-4 accent-primary-400"
              checked={allowAll}
              onChange={(event) => toggleAll(event.target.checked)}
            />
          </label>

          {!allowAll && (
            <div className="grid gap-2 sm:grid-cols-2">
              {libraries.map((library) => (
                <label key={library.id} className="flex min-w-0 items-center gap-3 border-b border-gray-100 px-1 py-2">
                  <input
                    type="checkbox"
                    className="h-4 w-4 shrink-0 accent-primary-400"
                    checked={selectedIDs.includes(library.id)}
                    onChange={() => toggleLibrary(library.id)}
                  />
                  <span className="min-w-0">
                    <span className="block truncate text-sm text-ink-600">{library.name}</span>
                    <span className="block truncate text-xs text-ink-50">{library.type}</span>
                  </span>
                </label>
              ))}
              {libraries.length === 0 && <p className="text-sm text-ink-50">暂无可分配的媒体库</p>}
            </div>
          )}

          {!allowAll && selectedIDs.length === 0 && (
            <p className="text-xs text-red-500">请至少选择一个媒体库，或允许访问全部媒体库。</p>
          )}
          {unavailableIDs.length > 0 && (
            <p className="text-xs text-amber-600">
              当前配置中有 {unavailableIDs.length} 个已不存在的媒体库，保存后将自动移除。
            </p>
          )}
        </div>

        <div className="flex justify-end gap-2 border-t border-gray-200 px-4 py-3">
          <button type="button" className="rounded-lg border border-gray-300 px-3 py-2 text-sm text-ink-100" onClick={onClose} disabled={saving}>
            取消
          </button>
          <button type="button" className="neon-button inline-flex items-center gap-2" onClick={save} disabled={saving || !canSave}>
            <Save size={15} />
            {saving ? '保存中' : '保存'}
          </button>
        </div>
      </div>
    </div>
  )
}
