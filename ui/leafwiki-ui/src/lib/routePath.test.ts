import { describe, expect, it, vi } from 'vitest'
import {
  buildViewUrl,
  buildEditUrl,
  buildHistoryUrl,
  buildPermalinkPath,
  routeToWikiPath,
  withBasePath,
} from './routePath'

describe('Japanese wiki URLs', () => {
  it('prefixes domain paths without changing hierarchy or literal ja slugs', () => {
    expect(buildViewUrl('')).toBe('/ja/')
    expect(buildViewUrl('top-level-domain')).toBe('/ja/top-level-domain')
    expect(buildViewUrl('/folder/page')).toBe('/ja/folder/page')
    expect(buildViewUrl('ja/page')).toBe('/ja/ja/page')
    expect(buildEditUrl('folder/page')).toBe('/ja/e/folder/page')
    expect(buildHistoryUrl('folder/page')).toBe('/ja/history/folder/page')
    expect(buildPermalinkPath('id', 'page')).toBe('/ja/p/id/page')
  })
  it('extracts lookup paths from wiki routes', () => {
    expect(routeToWikiPath('/ja/')).toBe('/')
    expect(routeToWikiPath('/ja/page')).toBe('/page')
    expect(routeToWikiPath('/ja/ja/page')).toBe('/ja/page')
    expect(routeToWikiPath('/ja/e/folder/page')).toBe('/folder/page')
    expect(routeToWikiPath('/ja/history/folder/page')).toBe('/folder/page')
  })
  it('does not prefix system or asset URLs via withBasePath', () => {
    for (const path of [
      '/api/pages',
      '/api/health',
      '/login',
      '/settings',
      '/assets/a.png',
    ])
      expect(withBasePath(path)).toBe(path)
  })
  it('keeps deployment base paths separate from the wiki prefix', async () => {
    vi.resetModules()
    vi.doMock('./config', () => ({ BASE_PATH: '/wiki' }))
    try {
      const routes = await import('./routePath')
      expect(routes.withBasePath(routes.buildViewUrl('ja/page'))).toBe(
        '/wiki/ja/ja/page',
      )
      expect(routes.routeToWikiPath('/wiki/ja/e/ja/page')).toBe('/ja/page')
      expect(routes.withBasePath('/api/pages')).toBe('/wiki/api/pages')
    } finally {
      vi.doUnmock('./config')
      vi.resetModules()
    }
  })
})
