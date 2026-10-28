import { useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { systemAPI } from '../api/system'

export function useSystemInfo() {
  const [info, setInfo] = useState<Awaited<ReturnType<typeof systemAPI.info>> | null>(null)

  useEffect(() => {
    let cancelled = false
    systemAPI.info()
      .then((value) => { if (!cancelled) setInfo(value) })
      .catch((error: unknown) => {
        if (cancelled) return
        const message = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
          || (error instanceof Error ? error.message : '请求失败')
        toast.error(`页面功能配置加载失败：${message}`)
      })
    return () => { cancelled = true }
  }, [])

  return info
}
