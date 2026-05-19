/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import generatedBrandBootstrap, {
  type BrandBootstrap,
} from '@/generated/brand-bootstrap'
import { DEFAULT_LOGO, DEFAULT_SYSTEM_NAME } from '@/lib/constants'
import { applyFaviconToDom } from '@/lib/dom-utils'

export type RuntimeBrand = Partial<
  Pick<BrandBootstrap, 'systemName' | 'logo' | 'description'>
>

function trimString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function normalizeRuntimeBrand(value: unknown): RuntimeBrand | null {
  if (!value || typeof value !== 'object') return null
  const source = value as Record<string, unknown>
  const systemName = trimString(source.systemName)
  const logo = trimString(source.logo)
  const description = trimString(source.description)

  if (!systemName && !logo && !description) return null

  return {
    ...(systemName ? { systemName } : {}),
    ...(logo ? { logo } : {}),
    ...(description ? { description } : {}),
  }
}

export function getBuildBrandBootstrap(): RuntimeBrand {
  const inlineBrand =
    typeof window !== 'undefined'
      ? normalizeRuntimeBrand(window.__APP_BRAND__)
      : null

  return (
    inlineBrand ??
    normalizeRuntimeBrand(generatedBrandBootstrap) ?? {
      systemName: DEFAULT_SYSTEM_NAME,
      logo: DEFAULT_LOGO,
    }
  )
}

export function getBrandFromStatus(status: unknown): RuntimeBrand | null {
  if (!status || typeof status !== 'object') return null
  const data = status as Record<string, unknown>
  const systemName = trimString(data.system_name)
  const logo = trimString(data.logo)

  if (!systemName && !logo) return null

  return {
    ...(systemName ? { systemName } : {}),
    ...(logo ? { logo } : {}),
  }
}

export function getCachedStatusBrand(): RuntimeBrand | null {
  try {
    if (typeof window === 'undefined') return null
    const saved = window.localStorage.getItem('status')
    if (!saved) return null
    return getBrandFromStatus(JSON.parse(saved))
  } catch {
    return null
  }
}

export function patchCachedStatus(patch: Record<string, unknown>) {
  try {
    if (typeof window === 'undefined') return
    const saved = window.localStorage.getItem('status')
    const current =
      saved && typeof saved === 'string'
        ? (JSON.parse(saved) as Record<string, unknown>)
        : {}
    window.localStorage.setItem(
      'status',
      JSON.stringify({ ...current, ...patch })
    )
  } catch {
    /* empty */
  }
}

function setMetaContent(name: string, content: string) {
  if (typeof document === 'undefined') return
  const meta = document.querySelector(
    `meta[name="${name}"]`
  ) as HTMLMetaElement | null
  if (meta) {
    meta.setAttribute('content', content)
    return
  }

  const next = document.createElement('meta')
  next.name = name
  next.content = content
  document.head.appendChild(next)
}

export function applyBrandToDom(brand: RuntimeBrand | null | undefined) {
  if (!brand || typeof document === 'undefined') return

  if (brand.systemName) {
    document.title = brand.systemName
    setMetaContent('title', brand.systemName)
  }

  if (brand.description) {
    setMetaContent('description', brand.description)
  }

  if (brand.logo) {
    applyFaviconToDom(brand.logo)
  }
}
