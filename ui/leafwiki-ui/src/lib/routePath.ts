import { BASE_PATH } from './config'

/**
 * Browser/router URL helpers.
 *
 * This file is responsible for route URLs that may include the configured
 * frontend base path and the viewer/editor route prefixes.
 *
 * It should not contain wiki-domain rules such as parent-page calculation or
 * Markdown link resolution. Those belong in `wikiPath.ts`.
 */
export function normalizeBasePath(base: string | undefined | null): string {
  if (!base || base === '/') return ''
  base = base.startsWith('/') ? base : `/${base}`
  return base.replace(/\/+$/, '')
}

const BASE = normalizeBasePath(BASE_PATH)

/** Ensures a pathname is represented as a route path with a leading slash. */
function ensureLeadingSlash(pathname: string): string {
  if (!pathname) {
    return '/'
  }
  return pathname.startsWith('/') ? pathname : `/${pathname}`
}

/** Adds the configured router base path to an already computed route path. */
export function withBasePath(p: string): string {
  if (!BASE) return p
  return BASE + (p.startsWith('/') ? p : `/${p}`)
}

/**
 * Removes the configured router base path from a browser pathname.
 *
 * Returns `null` when the pathname does not belong to the configured base.
 */
export function stripBasePath(pathname: string): string | null {
  pathname = ensureLeadingSlash(pathname)
  if (!BASE) return pathname
  if (pathname === BASE) return '/'
  if (pathname.startsWith(BASE + '/')) {
    const stripped = pathname.slice(BASE.length)
    return ensureLeadingSlash(stripped)
  }
  return null
}

/** Japanese wiki URL prefix; this is routing, not a database path segment. */
export const WIKI_ROOT = '/ja/'

/** Convert a domain path (including a literal `ja` slug) to a viewer URL. */
export function buildViewUrl(path: string): string {
  return WIKI_ROOT + path.replace(/^\/+/, '')
}

/** Extract a domain path from a browser/router URL. */
export function routeToWikiPath(pathname: string): string {
  const path = stripBasePath(pathname) ?? ensureLeadingSlash(pathname)
  for (const prefix of ['/ja/e', '/ja/history', '/ja']) {
    if (path === prefix || path === prefix + '/') return '/'
    if (path.startsWith(prefix + '/')) return path.slice(prefix.length)
  }
  return path
}

export function buildEditUrl(path: string): string {
  return '/ja/e/' + path.replace(/^\/+/, '')
}

export function buildHistoryUrl(path: string): string {
  return '/ja/history/' + path.replace(/^\/+/, '')
}

export function buildPermalinkPath(id: string, slug?: string): string {
  const path = '/ja/p/' + encodeURIComponent(id)
  return slug?.trim() ? path + '/' + encodeURIComponent(slug.trim()) : path
}

export function buildBrowserEditUrl(pathname: string): string {
  return withBasePath(buildEditUrl(pathname))
}
