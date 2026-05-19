import fs from 'node:fs'
import { defineConfig, loadEnv, type RsbuildPlugin } from '@rsbuild/core'
import { pluginReact } from '@rsbuild/plugin-react'
import { tanstackRouter } from '@tanstack/router-plugin/rspack'
import path from 'path'
import { fileURLToPath } from 'url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const brandBootstrapPath = path.resolve(
  __dirname,
  './src/generated/brand-bootstrap.json'
)

type BrandBootstrap = {
  systemName: string
  logo: string
  description: string
  source: string
  generatedAt: string | null
}

const defaultBrandBootstrap: BrandBootstrap = {
  systemName: 'New API',
  logo: '/logo.png',
  description: 'Unified AI API gateway and admin dashboard.',
  source: 'default',
  generatedAt: null,
}

function readBrandBootstrap(): BrandBootstrap {
  try {
    const raw = fs.readFileSync(brandBootstrapPath, 'utf8')
    const parsed = JSON.parse(raw) as Partial<BrandBootstrap>
    return {
      ...defaultBrandBootstrap,
      ...parsed,
      systemName: parsed.systemName?.trim() || defaultBrandBootstrap.systemName,
      logo: parsed.logo?.trim() || defaultBrandBootstrap.logo,
      description:
        parsed.description?.trim() || defaultBrandBootstrap.description,
    }
  } catch {
    return defaultBrandBootstrap
  }
}

function escapeHtml(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;')
}

function serializeInlineScriptValue(value: unknown): string {
  return JSON.stringify(value)
    .replaceAll('<', '\\u003c')
    .replaceAll('>', '\\u003e')
    .replaceAll('&', '\\u0026')
    .replaceAll('\u2028', '\\u2028')
    .replaceAll('\u2029', '\\u2029')
}

function upsertHeadTag(html: string, tagHtml: string): string {
  if (!/<\/head>/i.test(html)) return `${tagHtml}\n${html}`
  return html.replace(/<\/head>/i, `    ${tagHtml}\n  </head>`)
}

function replaceMetaContent(
  html: string,
  name: string,
  content: string
): string {
  const escaped = escapeHtml(content)
  const pattern = new RegExp(`<meta\\s+name=["']${name}["'][^>]*>`, 'i')
  if (!pattern.test(html)) {
    return upsertHeadTag(
      html,
      `<meta name="${escapeHtml(name)}" content="${escaped}" />`
    )
  }
  return html.replace(pattern, (tag) => {
    if (/\scontent=["'][^"']*["']/i.test(tag)) {
      return tag.replace(/\scontent=["'][^"']*["']/i, ` content="${escaped}"`)
    }
    return tag.replace(/\/?>$/, ` content="${escaped}" />`)
  })
}

function replaceIconHref(html: string, href: string): string {
  const escaped = escapeHtml(href)
  const pattern = /<link\s+[^>]*rel=["'][^"']*\bicon\b[^"']*["'][^>]*>\s*/gi
  const next = html.replace(pattern, '')
  return upsertHeadTag(next, `<link rel="icon" href="${escaped}" />`)
}

function applyBrandToHtml(html: string, brand: BrandBootstrap): string {
  const title = escapeHtml(brand.systemName)
  let next = /<title>[\s\S]*?<\/title>/i.test(html)
    ? html.replace(/<title>[\s\S]*?<\/title>/i, `<title>${title}</title>`)
    : upsertHeadTag(html, `<title>${title}</title>`)

  next = replaceMetaContent(next, 'title', brand.systemName)
  next = replaceMetaContent(next, 'description', brand.description)
  next = replaceIconHref(next, brand.logo)

  const inlineScript = `<script id="brand-bootstrap">window.__APP_BRAND__=${serializeInlineScriptValue(brand)};</script>`
  if (/<script\s+id=["']brand-bootstrap["'][\s\S]*?<\/script>/i.test(next)) {
    return next.replace(
      /<script\s+id=["']brand-bootstrap["'][\s\S]*?<\/script>/i,
      inlineScript
    )
  }
  return upsertHeadTag(next, inlineScript)
}

function pluginBrandBootstrap(brand: BrandBootstrap): RsbuildPlugin {
  return {
    name: 'brand-bootstrap',
    setup(api) {
      api.modifyHTML((html) => applyBrandToHtml(html, brand))
    },
  }
}

export default defineConfig(({ envMode }) => {
  const env = loadEnv({ mode: envMode, prefixes: ['VITE_'] })
  const brandBootstrap = readBrandBootstrap()
  const serverUrl =
    process.env.VITE_REACT_APP_SERVER_URL ||
    env.rawPublicVars.VITE_REACT_APP_SERVER_URL ||
    'http://localhost:3000'

  const isProd = envMode === 'production'
  const backendProxyPrefixes = [
    '/api',
    '/v1',
    '/v1beta',
    '/pg',
    '/mj',
    '/fast/mj',
    '/relax/mj',
    '/turbo/mj',
    '/suno',
    '/kling',
    '/jimeng',
    '/dashboard/billing',
  ] as const
  const devProxy = Object.fromEntries(
    backendProxyPrefixes.map((key) => [
      key,
      { target: serverUrl, changeOrigin: true },
    ])
  ) as Record<string, { target: string; changeOrigin: boolean }>

  return {
    plugins: [pluginReact(), pluginBrandBootstrap(brandBootstrap)],
    // Rsbuild 2: replaces deprecated `performance.chunkSplit` (RSPack 2 aligned)
    splitChunks: {
      preset: 'default',
      cacheGroups: {
        'vendor-react': {
          test: /node_modules[\\/](react|react-dom)[\\/]/,
          name: 'vendor-react',
          chunks: 'all',
          priority: 0,
          enforce: true,
        },
        'vendor-ui-primitives': {
          test: /node_modules[\\/](@base-ui|@radix-ui)[\\/]/,
          name: 'vendor-ui-primitives',
          chunks: 'all',
          priority: 0,
          enforce: true,
        },
        'vendor-tanstack': {
          test: /node_modules[\\/]@tanstack[\\/]/,
          name: 'vendor-tanstack',
          chunks: 'all',
          priority: 0,
          enforce: true,
        },
      },
    },
    source: {
      entry: {
        index: './src/main.tsx',
      },
    },
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    html: {
      template: './index.html',
    },
    server: {
      host: '0.0.0.0',
      proxy: devProxy,
    },
    output: {
      // Production optimizations
      minify: isProd,
      target: 'web',
      distPath: {
        root: 'dist',
      },
      // Rely on Rsbuild default legalComments ("linked" → per-chunk *.LICENSE.txt) in all modes.
      // Do not set "none" in production: that strips minifier-preserved third-party notices and
      // extracted license files, which some distributions require for open-source compliance.
    },
    performance: {
      // Remove console in production
      removeConsole: isProd ? ['log'] : false,
      // Speed up repeated `rsbuild build` (local + CI when node_modules/.cache is preserved).
      // @see https://v2.rsbuild.dev/config/performance/build-cache
      buildCache: {
        cacheDigest: [process.env.VITE_REACT_APP_VERSION],
      },
    },
    tools: {
      rspack: {
        plugins: [
          tanstackRouter({
            target: 'react',
            // Dev: avoid per-route async chunks (reduces white flash on navigation + faster HMR feedback).
            // Prod: keep route-based code splitting.
            autoCodeSplitting: isProd,
          }),
        ],
      },
    },
  }
})
