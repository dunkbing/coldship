// @ts-check
import { defineConfig } from 'astro/config'
import starlight from '@astrojs/starlight'

// SITE_URL (e.g. https://docs.example.com) is the docs' public address. It is
// needed for the sitemap and canonical links; set it when building for deployment.
export default defineConfig({
  site: process.env.SITE_URL || undefined,
  integrations: [
    starlight({
      title: 'Hangar',
      description: 'A self-hosted app platform built for AI agents, with humans in control.',
      logo: { src: './src/assets/logo.svg' },
      favicon: '/favicon.svg',
      customCss: ['./src/styles/custom.css'],
      lastUpdated: false,
      sidebar: [
        {
          label: 'Getting started',
          items: ['getting-started/introduction', 'getting-started/quick-start'],
        },
        {
          label: 'Concepts',
          items: [
            'concepts/plan-and-apply',
            'concepts/guardrails',
            'concepts/debugging-and-rollback',
          ],
        },
        {
          label: 'Guides',
          items: ['guides/ai-agents', 'guides/build-from-git', 'guides/dashboard', 'guides/production'],
        },
        {
          label: 'Reference',
          items: ['reference/spec', 'reference/cli', 'reference/mcp-tools', 'reference/api', 'reference/errors'],
        },
        {
          label: 'Project',
          items: ['project/architecture', 'project/contributing', 'project/roadmap'],
        },
      ],
    }),
  ],
})
