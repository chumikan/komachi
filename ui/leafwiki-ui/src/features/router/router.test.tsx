import SettingsSectionGuard from '@/features/settings/SettingsSectionGuard'
import { settingsSections } from '@/lib/registries/settingsSectionRegistry'
import { isValidElement } from 'react'
import { Navigate, matchRoutes } from 'react-router'
import { describe, expect, it } from 'vitest'
import ExternalRedirect from '../auth/ExternalRedirect'
import { ForgotPasswordForm, LoginForm } from './lazy-routes'
import { createLeafWikiRouter } from './router'

function loginRouteElementType(authDisabled: boolean, loginUrl: string) {
  const router = createLeafWikiRouter(
    false,
    authDisabled,
    false,
    loginUrl,
    true,
  )
  const loginRoute = router.routes.find((route) => route.path === '/login')
  const element = loginRoute?.element
  if (!isValidElement(element)) {
    throw new Error('expected /login route to render an element')
  }
  return element.type
}

describe('createLeafWikiRouter /login route', () => {
  it('navigates home when auth is disabled, even if loginUrl is configured', () => {
    expect(loginRouteElementType(true, 'https://idp.example.com/login')).toBe(
      Navigate,
    )
  })

  it('redirects externally when loginUrl is configured and auth is enabled', () => {
    expect(loginRouteElementType(false, 'https://idp.example.com/login')).toBe(
      ExternalRedirect,
    )
  })

  it('renders the local login form otherwise', () => {
    expect(loginRouteElementType(false, '')).toBe(LoginForm)
  })
})

function forgotPasswordRouteElementType(smtpEnabled: boolean) {
  const router = createLeafWikiRouter(false, false, false, '', smtpEnabled)
  const route = router.routes.find((r) => r.path === '/forgot-password')
  const element = route?.element
  if (!isValidElement(element)) {
    throw new Error('expected /forgot-password route to render an element')
  }
  return element.type
}

describe('createLeafWikiRouter /forgot-password route', () => {
  it('redirects to /login when SMTP is not configured', () => {
    expect(forgotPasswordRouteElementType(false)).toBe(Navigate)
  })

  it('renders the forgot-password form when SMTP is configured', () => {
    expect(forgotPasswordRouteElementType(true)).toBe(ForgotPasswordForm)
  })
})

describe('createLeafWikiRouter /settings route', () => {
  it('redirects to / for a read-only viewer', () => {
    const router = createLeafWikiRouter(true, false, false, '', true)
    const route = router.routes.find((r) => r.path === '/settings')
    const element = route?.element
    if (!isValidElement(element)) {
      throw new Error('expected /settings route to render an element')
    }
    expect(element.type).toBe(Navigate)
  })

  it('redirects the compat /users route to /settings/users', () => {
    const router = createLeafWikiRouter(false, false, false, '', true)
    const route = router.routes.find((r) => r.path === '/users')
    const element = route?.element
    if (!isValidElement(element)) {
      throw new Error('expected /users route to render an element')
    }
    expect(element.type).toBe(Navigate)
    expect((element.props as { to: string }).to).toBe('/settings/users')
  })

  it('gates every settings section route through SettingsSectionGuard — regression for the pre-registry snapshots direct-URL bypass', () => {
    const router = createLeafWikiRouter(false, false, false, '', true)
    const settingsRoute = router.routes.find((r) => r.path === '/settings')
    const children = settingsRoute?.children ?? []

    for (const section of settingsSections) {
      const childRoute = children.find((c) => c.path === section.path)
      const element = childRoute?.element
      if (!isValidElement(element)) {
        throw new Error(
          `expected /settings/${section.path} route to render an element`,
        )
      }
      expect(element.type).toBe(SettingsSectionGuard)
    }
  })
})

describe('Japanese wiki route boundary', () => {
  it('keeps wiki and system routes separate without legacy slug redirects', () => {
    const router = createLeafWikiRouter(false, false, true, '', true)
    try {
      for (const [url, pattern] of [
        ['/ja/', '/ja/'],
        ['/ja/top-level-domain', '/ja/*'],
        ['/ja/folder/page', '/ja/*'],
        ['/ja/e/page', '/ja/e/*'],
        ['/ja/history/page', '/ja/history/*'],
        ['/ja/p/id/slug', '/ja/p/:id/:slug?'],
        ['/login', '/login'],
        ['/settings/users', 'users'],
        ['/top-level-domain', '*'],
        ['/e/page', '*'],
        ['/en/page', '*'],
      ]) {
        expect(matchRoutes(router.routes, url)?.at(-1)?.route.path).toBe(
          pattern,
        )
      }
      const root = router.routes.find((route) => route.path === '/')?.element
      expect(isValidElement(root) && (root.props as { to: string }).to).toBe(
        '/ja/',
      )
    } finally {
      router.dispose()
    }
  })
})
