import { useEffect } from 'react'

import { usePermissionStore } from '../stores/permissions'
import { useAuthStore } from '../stores/auth'

/**
 * usePermission hook - 检查用户是否拥有特定权限
 * 
 * @param key - 权限键名
 * @param options - 配置选项
 * @param options.autoFetch - 是否在权限未加载时自动获取（默认 true）
 * @returns boolean - 用户是否拥有该权限
 * 
 * @example
 * ```tsx
 * function MyComponent() {
 *   const canEdit = usePermission('can_edit_media')
 *   
 *   if (canEdit) {
 *     return <EditButton />
 *   }
 *   return null
 * }
 * ```
 */
export function usePermission(
  key: string,
  options: { autoFetch?: boolean } = {}
): boolean {
  const { autoFetch = true } = options
  const { hasPermission, isSuper, permissions, isLoading, fetchPermissions } = usePermissionStore()
  const role = useAuthStore((state) => state.user?.role)
  const isAuthenticated = useAuthStore((state) => state.token !== null)
  const hasSuperAccess = isSuper || role === 'admin'

  // 权限未加载时自动获取
  useEffect(() => {
    if (!hasSuperAccess && isAuthenticated && autoFetch && Object.keys(permissions).length === 0 && !isLoading) {
      fetchPermissions()
    }
  }, [autoFetch, fetchPermissions, hasSuperAccess, isAuthenticated, isLoading, permissions])

  // 超级用户有所有权限
  if (hasSuperAccess) {
    return true
  }

  // 权限未加载且未认证时，返回 false
  if (!isAuthenticated) {
    return false
  }

  return hasPermission(key)
}

/**
 * usePermissions hook - 获取所有权限
 * 
 * @returns 权限状态和检查函数
 * 
 * @example
 * ```tsx
 * function MyComponent() {
 *   const { permissions, isSuper, check } = usePermissions()
 *   
 *   if (isSuper) {
 *     return <AdminPanel />
 *   }
 *   
 *   return (
 *     <div>
 *       {check('can_view_dashboard') && <Dashboard />}
 *       {check('can_play_media') && <Player />}
 *     </div>
 *   )
 * }
 * ```
 */
export function usePermissions() {
  const { permissions, isSuper, isLoading, fetchPermissions } = usePermissionStore()
  const role = useAuthStore((state) => state.user?.role)
  const isAuthenticated = useAuthStore((state) => state.token !== null)

  const check = (key: string): boolean => {
    if (isSuper || role === 'admin') {
      return true
    }
    return permissions[key] === true
  }

  return {
    permissions,
    isSuper: isSuper || role === 'admin',
    isLoading,
    check,
    refetch: fetchPermissions,
    isAuthenticated,
  }
}

/**
 * usePermissionMany hook - 批量检查多个权限
 * 
 * @param keys - 权限键数组
 * @returns 每个权限的布尔值映射
 * 
 * @example
 * ```tsx
 * function MyComponent() {
 *   const perms = usePermissionMany([
 *     'can_edit_media',
 *     'can_manage_users',
 *     'can_access_settings',
 *   ])
 *   
 *   return (
 *     <div>
 *       {perms['can_edit_media'] && <EditButton />}
 *       {perms['can_manage_users'] && <UserManagement />}
 *     </div>
 *   )
 * }
 * ```
 */
export function usePermissionMany(keys: string[]): Record<string, boolean> {
  const { check } = usePermissions()
  
  return keys.reduce((acc, key) => {
    acc[key] = check(key)
    return acc
  }, {} as Record<string, boolean>)
}
