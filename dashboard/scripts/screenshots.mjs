// Captures dashboard screenshots for the docs. Run via scripts/screenshots.sh,
// which seeds a demo server and runs this inside the Playwright Docker image.
import { chromium } from 'playwright'

const base = process.env.HANGAR_URL ?? 'http://127.0.0.1:7272'
const token = process.env.HANGAR_TOKEN
const out = process.env.OUT ?? '.'
if (!token) throw new Error('HANGAR_TOKEN is required')

const browser = await chromium.launch()

async function session(colorScheme) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2, colorScheme })
  await ctx.addInitScript((t) => localStorage.setItem('hangar.token', t), token)
  return ctx
}

async function shot(page, path, name, waitFor, opts = {}) {
  await page.goto(base + path)
  await page.getByText(waitFor, { exact: false }).first().waitFor({ timeout: 15000 })
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${out}/${name}.png`, fullPage: opts.fullPage ?? false })
  console.log('captured', name)
}

// Login screen (no token).
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2, colorScheme: 'light' })
  const page = await ctx.newPage()
  await page.goto(base + '/')
  await page.getByText('Sign in with an API token').waitFor()
  await page.screenshot({ path: `${out}/login.png` })
  console.log('captured login')
  await ctx.close()
}

const light = await session('light')
const page = await light.newPage()
await shot(page, '/apps', 'apps', 'demo-worker')
await shot(page, '/apps/demo-worker?tab=diagnose', 'app-diagnose', 'port_mismatch')
await shot(page, '/apps/demo-worker?tab=deployments', 'app-deployments', 'Upgrade worker to v2')
await shot(page, '/apps/demo-worker?tab=logs', 'app-logs', 'DATABASE_URL')
await shot(page, '/approvals', 'approvals', 'volumes.cache', { fullPage: true })
await shot(page, '/audit', 'audit', 'plan.apply')
await light.close()

const dark = await session('dark')
const dpage = await dark.newPage()
await shot(dpage, '/apps', 'apps-dark', 'demo-worker')
await dark.close()

await browser.close()
