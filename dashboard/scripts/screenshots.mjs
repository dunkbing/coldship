// Captures dashboard screenshots for the docs. Run via scripts/screenshots.sh,
// which seeds a demo server and runs this inside the Playwright Docker image.
import { chromium } from 'playwright'

const base = process.env.HANGAR_URL ?? 'http://127.0.0.1:7272'
const token = process.env.HANGAR_TOKEN
const out = process.env.OUT ?? '.'
if (!token) throw new Error('HANGAR_TOKEN is required')

const browser = await chromium.launch()

// Sign in the way the dashboard does: exchange the token for a session cookie.
async function session(colorScheme) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2, colorScheme })
  const res = await ctx.request.post(base + '/v1/sessions', { data: { token } })
  if (!res.ok()) throw new Error(`sign-in failed: ${res.status()} ${await res.text()}`)
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
await shot(page, '/apps/demo-site?tab=diagnose', 'app-git-diagnose', 'build_failed')
// Deployments of the Git-built app, with the failed build's log expanded.
await page.goto(base + '/apps/demo-site?tab=deployments')
await page.getByRole('button', { name: 'Build log' }).first().click()
await page.getByText('npm run build').first().waitFor({ timeout: 15000 })
await page.waitForTimeout(400)
await page.screenshot({ path: `${out}/app-git-deployments.png`, fullPage: true })
console.log('captured app-git-deployments')
await shot(page, '/approvals', 'approvals', 'volumes.cache', { fullPage: true })
await shot(page, '/audit', 'audit', 'plan.apply')
await light.close()

const dark = await session('dark')
const dpage = await dark.newPage()
await shot(dpage, '/apps', 'apps-dark', 'demo-worker')
await dark.close()

await browser.close()
