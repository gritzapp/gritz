// Theme preference. The stored preference is light, dark, or system (follow
// the OS). The resolved theme is applied as the `dark` class on <html>, which
// the `dark:` variant and the token overrides in index.css key off. index.html
// applies the same class with an inline script before first paint so a dark
// page never flashes light; keep THEME_STORAGE_KEY in sync with it.

export type Theme = 'light' | 'dark' | 'system'

export const THEME_STORAGE_KEY = 'gritz-theme'

const media = window.matchMedia('(prefers-color-scheme: dark)')
const listeners = new Set<() => void>()

function readTheme(): Theme {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY)
    if (v === 'light' || v === 'dark') {
      return v
    }
  } catch {
    // Storage unavailable: fall through to system.
  }
  return 'system'
}

let current: Theme = readTheme()

function apply(): void {
  const dark = current === 'dark' || (current === 'system' && media.matches)
  document.documentElement.classList.toggle('dark', dark)
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
}

function notify(): void {
  apply()
  for (const l of listeners) {
    l()
  }
}

media.addEventListener('change', notify)

// Another tab changed the preference.
window.addEventListener('storage', (e) => {
  if (e.key === THEME_STORAGE_KEY) {
    current = readTheme()
    notify()
  }
})

apply()

export function getTheme(): Theme {
  return current
}

export function setTheme(theme: Theme): void {
  current = theme
  try {
    if (theme === 'system') {
      localStorage.removeItem(THEME_STORAGE_KEY)
    } else {
      localStorage.setItem(THEME_STORAGE_KEY, theme)
    }
  } catch {
    // Storage unavailable: the choice lasts for this page load only.
  }
  notify()
}

export function subscribeTheme(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}
