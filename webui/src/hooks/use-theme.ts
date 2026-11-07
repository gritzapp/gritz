import { useSyncExternalStore } from 'react'
import { getTheme, setTheme, subscribeTheme, type Theme } from '@/lib/theme'

export function useTheme(): [Theme, (theme: Theme) => void] {
  const theme = useSyncExternalStore(subscribeTheme, getTheme)
  return [theme, setTheme]
}
