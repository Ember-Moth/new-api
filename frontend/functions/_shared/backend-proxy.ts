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

interface PagesFunctionContext {
  request: Request
  env: {
    BACKEND_ORIGIN?: string
  }
}

type PagesFunction = (
  context: PagesFunctionContext
) => Response | Promise<Response>

const BODYLESS_METHODS = new Set(['GET', 'HEAD'])

export const onRequest: PagesFunction = async (context) => {
  const backendOrigin = normalizeBackendOrigin(context.env.BACKEND_ORIGIN)
  if (!backendOrigin) {
    return jsonError('BACKEND_ORIGIN is not configured', 500)
  }

  let targetUrl: URL
  try {
    targetUrl = buildTargetUrl(context.request.url, backendOrigin)
  } catch {
    return jsonError('BACKEND_ORIGIN is invalid', 500)
  }

  const incomingUrl = new URL(context.request.url)
  const headers = createProxyHeaders(context.request, incomingUrl)
  const response = await fetch(
    new Request(targetUrl.toString(), {
      method: context.request.method,
      headers,
      body: BODYLESS_METHODS.has(context.request.method)
        ? undefined
        : context.request.body,
      redirect: 'manual',
    })
  )

  const responseHeaders = new Headers(response.headers)
  responseHeaders.set('Cache-Control', 'no-store')
  rewriteBackendLocation(responseHeaders, targetUrl.origin, incomingUrl.origin)

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: responseHeaders,
  })
}

function normalizeBackendOrigin(value?: string): string {
  return (value || '').trim().replace(/\/+$/, '')
}

function buildTargetUrl(requestUrl: string, backendOrigin: string): URL {
  const incomingUrl = new URL(requestUrl)
  const targetUrl = new URL(backendOrigin)
  const basePath = targetUrl.pathname.replace(/\/+$/, '')
  targetUrl.pathname = `${basePath}${incomingUrl.pathname}`
  targetUrl.search = incomingUrl.search
  targetUrl.hash = ''
  return targetUrl
}

function createProxyHeaders(request: Request, incomingUrl: URL): Headers {
  const headers = new Headers(request.headers)
  headers.delete('host')
  headers.set('X-Forwarded-Host', incomingUrl.host)
  headers.set('X-Forwarded-Proto', incomingUrl.protocol.replace(':', ''))

  const clientIp = request.headers.get('CF-Connecting-IP')
  if (clientIp) {
    headers.set('X-Forwarded-For', clientIp)
    headers.set('X-Real-IP', clientIp)
  }

  return headers
}

function rewriteBackendLocation(
  headers: Headers,
  backendOrigin: string,
  frontendOrigin: string
): void {
  const location = headers.get('Location')
  if (!location) {
    return
  }

  try {
    const locationUrl = new URL(location, backendOrigin)
    if (locationUrl.origin !== backendOrigin) {
      return
    }
    headers.set(
      'Location',
      `${frontendOrigin}${locationUrl.pathname}${locationUrl.search}${locationUrl.hash}`
    )
  } catch {
    /* keep the original Location header */
  }
}

function jsonError(message: string, status: number): Response {
  return Response.json(
    {
      success: false,
      message,
    },
    {
      status,
      headers: {
        'Cache-Control': 'no-store',
      },
    }
  )
}
