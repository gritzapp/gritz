import { useEffect } from 'react'
import { Monitor, Moon, Sun } from 'lucide-react'
import { useTernaryDarkMode, type TernaryDarkMode } from 'usehooks-ts'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

// The stored preference is light, dark, or system (follow the OS). index.html
// reads the same key before first paint so a dark page never flashes light;
// keep the two in sync. usehooks-ts stores the value JSON-encoded.
const THEME_OPTIONS = { localStorageKey: 'gritz-theme' }

const OPTIONS = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
] as const

// useApplyTheme applies the resolved theme as the `dark` class on <html>,
// which the `dark:` variant and the token overrides in index.css key off.
// Call it once, from the root layout.
export function useApplyTheme() {
  const { isDarkMode } = useTernaryDarkMode(THEME_OPTIONS)
  useEffect(() => {
    document.documentElement.classList.toggle('dark', isDarkMode)
    document.documentElement.style.colorScheme = isDarkMode ? 'dark' : 'light'
  }, [isDarkMode])
}

export function ThemeToggle() {
  const { ternaryDarkMode, setTernaryDarkMode } = useTernaryDarkMode(THEME_OPTIONS)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
        title="Theme"
        aria-label="Theme"
      >
        <Sun className="h-4 w-4 dark:hidden" />
        <Moon className="hidden h-4 w-4 dark:block" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup
          value={ternaryDarkMode}
          onValueChange={(v) => setTernaryDarkMode(v as TernaryDarkMode)}
        >
          {OPTIONS.map(({ value, label, icon: Icon }) => (
            <DropdownMenuRadioItem key={value} value={value}>
              <Icon />
              {label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
