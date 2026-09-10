import { describe, expect, it } from 'vitest'
import {
  getDeleteRedirectRoutePath,
  getParentWikiRoutePath,
  getWikiTargetRoutePath,
  normalizeWikiRoutePath,
  resolveWikiLinkPath,
  toWikiLookupPath,
} from './wikiPath'

describe('normalizeWikiRoutePath', () => {
  it('adds a leading slash and strips trailing slashes', () => {
    expect(normalizeWikiRoutePath('docs/guide')).toBe('/docs/guide')
    expect(normalizeWikiRoutePath('/docs/guide/')).toBe('/docs/guide')
  })

  it('strips query and hash fragments', () => {
    expect(normalizeWikiRoutePath('/docs/guide?q=1#section')).toBe(
      '/docs/guide',
    )
  })

  it('keeps root as /', () => {
    expect(normalizeWikiRoutePath('/')).toBe('/')
  })
})

describe('toWikiLookupPath', () => {
  it('converts absolute wiki routes to tree lookup keys', () => {
    expect(toWikiLookupPath('/docs/getting-started')).toBe(
      'docs/getting-started',
    )
    expect(toWikiLookupPath('/')).toBe('')
  })
})

describe('getWikiTargetRoutePath', () => {
  it('maps edit and history routes back to the view path', () => {
    expect(getWikiTargetRoutePath('/ja/e/docs/guide')).toBe('/docs/guide')
    expect(getWikiTargetRoutePath('/ja/history/docs/guide')).toBe('/docs/guide')
    expect(getWikiTargetRoutePath('/ja/history')).toBe('/')
    expect(getWikiTargetRoutePath('/ja/docs/guide')).toBe('/docs/guide')
  })
})

describe('resolveWikiLinkPath', () => {
  it('resolves relative links with page-as-folder semantics', () => {
    expect(resolveWikiLinkPath('/docs/guide', 'setup')).toBe(
      '/docs/guide/setup',
    )
    expect(resolveWikiLinkPath('/docs/guide', '../other')).toBe('/docs/other')
    expect(resolveWikiLinkPath('/docs/guide', './child/page')).toBe(
      '/docs/guide/child/page',
    )
  })

  it('resolves absolute wiki paths from the site root', () => {
    expect(resolveWikiLinkPath('/docs/guide', '/root-page')).toBe('/root-page')
  })
})

describe('getParentWikiRoutePath', () => {
  it('returns / for top-level pages', () => {
    expect(getParentWikiRoutePath('/docs')).toBe('/')
    expect(getParentWikiRoutePath('/')).toBe('/')
  })

  it('returns the parent for nested pages', () => {
    expect(getParentWikiRoutePath('/docs/guide/setup')).toBe('/docs/guide')
  })
})

describe('getDeleteRedirectRoutePath', () => {
  it('redirects to the parent when the deleted page is open', () => {
    expect(getDeleteRedirectRoutePath('/ja/docs/guide', '/docs/guide')).toBe(
      '/ja/docs',
    )
  })

  it('redirects from editor and history routes for the deleted page', () => {
    expect(getDeleteRedirectRoutePath('/ja/e/docs/guide', '/docs/guide')).toBe(
      '/ja/docs',
    )
    expect(
      getDeleteRedirectRoutePath('/ja/history/docs/guide', '/docs/guide'),
    ).toBe('/ja/docs')
  })

  it('redirects to the parent when a nested route under the deleted page is open', () => {
    expect(
      getDeleteRedirectRoutePath('/ja/docs/guide/setup', '/docs/guide'),
    ).toBe('/ja/docs')
  })

  it('keeps the current route when another page is open', () => {
    expect(getDeleteRedirectRoutePath('/ja/docs/other', '/docs/guide')).toBe(
      '/ja/docs/other',
    )
  })
})
