import { useState } from 'react'

// Small screens always start collapsed: the expanded sidebar overlays the
// timeline, so it's opened on demand and not remembered. On larger screens the
// collapse preference survives navigation and reloads, defaulting to expanded.
const SIDEBAR_COLLAPSED_KEY = 'task-sidebar-collapsed'
const SMALL_SCREEN_QUERY = '(max-width: 767px)'

const isSmallScreen = () => window.matchMedia(SMALL_SCREEN_QUERY).matches

function initialSidebarCollapsed(): boolean {
  if (isSmallScreen()) return true
  return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === 'true'
}

// useTaskSidebarCollapsed is the task sidebar's collapse state and its toggle.
export function useTaskSidebarCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(initialSidebarCollapsed)
  const toggle = () =>
    setCollapsed((collapsed) => {
      if (!isSmallScreen()) localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(!collapsed))
      return !collapsed
    })
  return [collapsed, toggle]
}
