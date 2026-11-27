import { useState } from 'react'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'
import { Check, ChevronsUpDown } from 'lucide-react'

// matchesEveryWord is the Combobox's search: an option matches when each word
// typed appears somewhere in its label, value or keywords. cmdk's own fuzzy
// match accepts any letters in order, which finds far too much in a long list
// such as the timezones.
function matchesEveryWord(value: string, search: string, keywords?: string[]): number {
  const haystack = [value, ...(keywords ?? [])].join(' ').toLowerCase()
  const words = search.toLowerCase().split(/\s+/).filter(Boolean)
  return words.every((word) => haystack.includes(word)) ? 1 : 0
}

// ComboboxOption is one choice of a Combobox. Options that share a group are
// listed under it as a heading; those with none come first. The search matches
// the label, the value and the keywords.
export interface ComboboxOption {
  value: string
  label: string
  group?: string
  icon?: React.ReactNode
  // Shown muted after the label in the list.
  description?: string
  keywords?: string[]
}

// Combobox picks one option from a searchable list, grouped when its options
// name groups. It is for lists that grow with the org or are long by nature;
// a handful of fixed choices reads better as a Select.
export function Combobox({
  id,
  options,
  value,
  onChange,
  placeholder,
  searchPlaceholder = 'Search...',
  emptyText = 'Nothing found.',
  disabled,
  className,
  'aria-label': ariaLabel,
}: {
  id?: string
  options: ComboboxOption[]
  value: string
  onChange: (value: string) => void
  placeholder: string
  searchPlaceholder?: string
  emptyText?: string
  disabled?: boolean
  className?: string
  'aria-label'?: string
}) {
  const [open, setOpen] = useState(false)
  const selected = options.find((o) => o.value === value)
  // Groups in the order their first option appears, ungrouped options first.
  const groups = [...new Set(options.map((o) => o.group ?? ''))].sort(
    (a, b) => Number(a !== '') - Number(b !== ''),
  )

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          aria-label={ariaLabel}
          disabled={disabled}
          className={cn('w-full justify-between px-3 font-normal', className)}
        >
          {selected ? (
            <span className="flex min-w-0 items-center gap-2">
              {selected.icon}
              <span className="truncate">{selected.label}</span>
            </span>
          ) : (
            <span className="text-muted-foreground truncate">{placeholder}</span>
          )}
          <ChevronsUpDown className="size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) min-w-72 p-0">
        <Command filter={matchesEveryWord}>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            {groups.map((group) => (
              <CommandGroup key={group} heading={group || undefined}>
                {options
                  .filter((o) => (o.group ?? '') === group)
                  .map((o) => (
                    <CommandItem
                      key={o.value}
                      value={`${o.label} ${o.value}`}
                      keywords={o.keywords}
                      onSelect={() => {
                        onChange(o.value)
                        setOpen(false)
                      }}
                    >
                      {o.icon}
                      <span className="shrink-0 whitespace-nowrap">{o.label}</span>
                      {o.description && (
                        <span className="text-muted-foreground min-w-0 flex-1 truncate text-xs">
                          {o.description}
                        </span>
                      )}
                      <Check
                        className={cn(
                          'ml-auto size-4 shrink-0',
                          o.value === value ? 'opacity-100' : 'opacity-0',
                        )}
                      />
                    </CommandItem>
                  ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
