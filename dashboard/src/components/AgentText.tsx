import { useRef, useState, type ReactNode } from 'react'
import { Check, Copy } from 'lucide-react'
import Markdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

// AgentText renders an agent's Markdown (GitHub flavored). Nothing is
// treated as HTML, unsafe link schemes are dropped, and images are shown
// as links: loading one would let a prompt-injected agent send data to
// any server just by being read.
export function AgentText({ text }: { text: string }) {
  return (
    <div className="agent-md text-sm leading-relaxed wrap-anywhere">
      <Markdown remarkPlugins={[remarkGfm]} components={mdComponents} skipHtml>
        {text}
      </Markdown>
    </div>
  )
}

const mdComponents: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer nofollow" className="text-primary underline underline-offset-2 hover:no-underline">
      {children}
    </a>
  ),
  img: ({ src, alt }) => (
    <span className="text-muted-foreground">
      [image{alt ? `: ${alt}` : ''}
      {typeof src === 'string' && src ? ` (${src})` : ''}]
    </span>
  ),
  pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
  code: ({ className, children }) =>
    className ? (
      <code className={className}>{children}</code>
    ) : (
      <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.8125rem]">{children}</code>
    ),
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto rounded-lg border">
      <table className="w-full text-left text-[0.8125rem]">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border-b bg-muted/50 px-2.5 py-1.5 font-medium">{children}</th>,
  td: ({ children }) => <td className="border-b px-2.5 py-1.5 align-top last:border-b-0">{children}</td>,
}

function CodeBlock({ children }: { children: ReactNode }) {
  const ref = useRef<HTMLPreElement>(null)
  const [copied, setCopied] = useState(false)
  return (
    <div className="group relative my-2">
      <pre ref={ref} className="overflow-x-auto rounded-lg border bg-muted p-3 font-mono text-[0.8125rem] leading-normal whitespace-pre">
        {children}
      </pre>
      <button
        type="button"
        aria-label="Copy"
        onClick={() => {
          navigator.clipboard?.writeText(ref.current?.innerText ?? '').then(() => {
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
          })
        }}
        className="absolute top-1.5 right-1.5 rounded-md border bg-card p-1 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100"
      >
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </button>
    </div>
  )
}
