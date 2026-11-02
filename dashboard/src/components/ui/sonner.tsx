import { Toaster as Sonner, type ToasterProps } from 'sonner'
import { CircleCheckIcon, InfoIcon, TriangleAlertIcon, OctagonXIcon, Loader2Icon } from 'lucide-react'

// The theme follows the OS, like the rest of the dashboard (see main.tsx).
const Toaster = ({ ...props }: ToasterProps) => (
  <Sonner
    theme="system"
    className="toaster group"
    icons={{
      success: <CircleCheckIcon className="size-4 text-ok" />,
      info: <InfoIcon className="size-4 text-info" />,
      warning: <TriangleAlertIcon className="size-4 text-warn" />,
      error: <OctagonXIcon className="size-4 text-destructive" />,
      loading: <Loader2Icon className="size-4 animate-spin" />,
    }}
    style={
      {
        '--normal-bg': 'var(--popover)',
        '--normal-text': 'var(--popover-foreground)',
        '--normal-border': 'var(--border)',
        '--border-radius': 'var(--radius)',
      } as React.CSSProperties
    }
    toastOptions={{ classNames: { toast: 'cn-toast', description: '!text-muted-foreground' } }}
    {...props}
  />
)

export { Toaster }
