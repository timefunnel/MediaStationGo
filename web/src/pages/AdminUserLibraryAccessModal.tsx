import { useState } from 'react'
import { LibraryBig, Save, X } from 'lucide-react'

import type { Library, User } from '../types'
import { AdminAccessSwitch } from './AdminAccessSwitch'

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

        <div className="space-y-4 p-4">
          <label className="flex items-center justify-between gap-4 border-b border-gray-200 pb-3">
            <span>
              <span className="block text-sm font-medium text-ink-600">全部媒体库</span>
              <span className="block text-xs text-ink-50">以后新建的媒体库也会自动开放</span>
            </span>
            <AdminAccessSwitch
              checked={allowAll}
              disabled={saving}
              label="允许访问全部媒体库"
              onChange={toggleAll}
            />
          </label>

          <div
            className={`h-64 overflow-y-auto rounded-lg border border-gray-200 p-2 transition-opacity ${
              allowAll ? 'bg-gray-50 opacity-60' : 'bg-white'
            }`}
            aria-disabled={allowAll}
          >
            <div className="grid gap-1 sm:grid-cols-2">
              {libraries.map((library) => (
                <div key={library.id} className="flex min-w-0 items-center justify-between gap-3 border-b border-gray-100 px-2 py-2.5">
                  <span className="min-w-0">
                    <span className="block truncate text-sm text-ink-600">{library.name}</span>
                    <span className="block truncate text-xs text-ink-50">{library.type}</span>
                  </span>
                  <AdminAccessSwitch
                    checked={selectedIDs.includes(library.id)}
                    disabled={allowAll || saving}
                    label={`${selectedIDs.includes(library.id) ? '关闭' : '开放'} ${library.name}`}
                    onChange={() => toggleLibrary(library.id)}
                  />
                </div>
              ))}
              {libraries.length === 0 && <p className="text-sm text-ink-50">暂无可分配的媒体库</p>}
            </div>
          </div>

          <div className="min-h-5 text-xs">
            {!allowAll && selectedIDs.length === 0 && (
              <p className="text-red-500">请至少选择一个媒体库，或允许访问全部媒体库。</p>
            )}
            {unavailableIDs.length > 0 && (
              <p className="text-amber-600">
                当前配置中有 {unavailableIDs.length} 个已不存在的媒体库，保存后将自动移除。
              </p>
            )}
          </div>
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
