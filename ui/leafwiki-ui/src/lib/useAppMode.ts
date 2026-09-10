// useAppMode returns the current application mode.
import { stripBasePath } from '@/lib/routePath'
import { useLocation } from 'react-router'

export type AppMode = 'edit' | 'history' | 'view' | 'dialog' | 'settings'

// based on the current route it will return the app mode
export function useAppMode(): AppMode {
  const location = useLocation()
  const pathname = stripBasePath(location.pathname) ?? location.pathname

  if (pathname.startsWith('/ja/e/')) {
    return 'edit'
  }

  if (
    pathname === '/ja/history' ||
    pathname === '/ja/history/' ||
    pathname.startsWith('/ja/history/')
  ) {
    return 'history'
  }

  if (pathname.startsWith('/settings')) {
    return 'settings'
  }

  return 'view'
}
