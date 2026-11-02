import { Bot, SquareTerminal } from 'lucide-react'
import { siClaude, siGooglegemini } from 'simple-icons'
import { cn } from '@/lib/utils'

// Logos (Simple Icons, CC0) where one exists; Codex and custom agents get
// plain marks.
const logos: Record<string, { path: string; hex: string } | undefined> = {
  claude: siClaude,
  gemini: siGooglegemini,
}

export const agentLabels: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  gemini: 'Gemini CLI',
  custom: 'Custom agent',
}

export const agentBlurbs: Record<string, string> = {
  claude: "Anthropic's coding agent. Needs an Anthropic API key.",
  codex: "OpenAI's coding agent. Needs an OpenAI API key.",
  gemini: "Google's agent, in ACP mode. Experimental. Needs a Gemini API key.",
  custom: 'Any agent that speaks the Agent Client Protocol on stdio, started with your command.',
}

export function AgentMark({ kind, size = 36 }: { kind: string; size?: number }) {
  const logo = logos[kind]
  const box = 'flex flex-none items-center justify-center rounded-lg border bg-card'
  if (logo)
    return (
      <span className={box} style={{ width: size, height: size }} aria-hidden>
        <svg viewBox="0 0 24 24" width={size * 0.58} height={size * 0.58} fill={`#${logo.hex}`}>
          <path d={logo.path} />
        </svg>
      </span>
    )
  const Icon = kind === 'codex' ? SquareTerminal : Bot
  return (
    <span className={cn(box, 'text-muted-foreground')} style={{ width: size, height: size }} aria-hidden>
      <Icon style={{ width: size * 0.55, height: size * 0.55 }} />
    </span>
  )
}

// toolName turns a tool's title into words: agents name Coldship's MCP
// tools like "mcp__coldship__coldship_list_apps", which reads as
// "List apps".
export function toolName(title: string): string {
  const m = /(?:^|_)coldship_([a-z0-9][a-z0-9_]*)$/.exec(title.trim())
  if (!m) return title
  const words = m[1].replace(/_/g, ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export function isColdshipTool(title: string): boolean {
  return /coldship_[a-z]/.test(title)
}

function parse(s: string): unknown {
  try {
    return JSON.parse(s)
  } catch {
    return undefined
  }
}

// inputSummary is a one-line "app: web · lines: 100" for flat inputs.
export function inputSummary(input?: string): string {
  if (!input) return ''
  const v = parse(input)
  if (!v || typeof v !== 'object' || Array.isArray(v)) return ''
  const parts: string[] = []
  for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
    if (x === null || x === undefined || x === '') continue
    if (typeof x === 'object') parts.push(`${k}: …`)
    else parts.push(`${k}: ${String(x)}`)
  }
  const s = parts.join(' · ')
  return s.length > 90 ? s.slice(0, 89) + '…' : s
}

// toolOutput unwraps an MCP result ({content: [{type: "text", text}]})
// and pretty-prints JSON, so people read data, not envelopes.
export function toolOutput(out?: string): string {
  if (!out) return ''
  let text = out
  const v = parse(out) as { content?: { type: string; text?: string }[] } | undefined
  if (v && Array.isArray(v.content)) {
    const texts = v.content.filter((c) => c.type === 'text' && c.text).map((c) => c.text!)
    if (texts.length) text = texts.join('\n')
  }
  const inner = parse(text)
  if (inner !== undefined && typeof inner === 'object') return JSON.stringify(inner, null, 2)
  return text
}
