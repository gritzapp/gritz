import { Bell } from 'lucide-react'
import { GithubIcon } from '@/components/github-icon'
import { AtlassianIcon } from '@/components/atlassian-icon'

// externalSourceStyle maps a source string — the persisted ExternalPayload.source
// for external events, or the URL-inferred source (sourceFromUrl) for links —
// onto the icon and label shown in the timeline and links list.
// github/jira are recognized; anything else (including empty) falls through to a
// generic "External" style.
export function externalSourceStyle(source: string): {
  icon: React.ReactNode
  label: string
} {
  switch (source.toLowerCase()) {
    case 'github':
      return {
        icon: <GithubIcon className="h-4 w-4" />,
        label: 'GitHub',
      }
    case 'jira':
      return {
        icon: <AtlassianIcon className="h-4 w-4" />,
        label: 'Jira',
      }
    default:
      return {
        icon: <Bell className="h-4 w-4" />,
        label: 'External',
      }
  }
}
