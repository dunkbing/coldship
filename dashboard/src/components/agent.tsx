import type { ReactNode } from 'react'
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

// Markdown, the small safe subset agents use: fenced code, inline code,
// bold and paragraphs. It builds React elements, so nothing in the text is
// ever treated as HTML.
export function AgentText({ text }: { text: string }) {
  const blocks: ReactNode[] = []
  const parts = text.split(/```[^\n]*\n?/)
  parts.forEach((part, i) => {
    if (i % 2 === 1) {
      blocks.push(
        <pre key={i} className="my-2 overflow-x-auto rounded-lg border bg-muted p-3 font-mono text-[0.8125rem] whitespace-pre">
          {part.replace(/\n$/, '')}
        </pre>,
      )
      return
    }
    part
      .split(/\n{2,}/)
      .filter((p) => p.trim())
      .forEach((p, j) => {
        blocks.push(
          <p key={`${i}-${j}`} className="my-1.5 whitespace-pre-wrap">
            {inline(p)}
          </p>,
        )
      })
  })
  return <div className="text-sm leading-relaxed wrap-anywhere">{blocks}</div>
}

function inline(s: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /(`[^`\n]+`|\*\*[^*\n]+\*\*)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(s.slice(last, m.index))
    const t = m[0]
    out.push(
      t.startsWith('`') ? (
        <code key={m.index} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.8125rem]">
          {t.slice(1, -1)}
        </code>
      ) : (
        <strong key={m.index}>{t.slice(2, -2)}</strong>
      ),
    )
    last = m.index + t.length
  }
  if (last < s.length) out.push(s.slice(last))
  return out
}
