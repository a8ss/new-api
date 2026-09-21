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
import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterAll, expect, test, vi } from 'vitest'

const backgroundImageUrl = 'https://example.com/login-background.jpg'
vi.stubEnv('VITE_LOGIN_BACKGROUND_IMAGE_URL', backgroundImageUrl)
afterAll(() => vi.unstubAllEnvs())

vi.mock('@tanstack/react-router', () => ({
  Link: (props: { children?: ReactNode }) => <a>{props.children}</a>,
  useSearch: () => ({}),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))
vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ status: { register_enabled: false } }),
}))
vi.mock('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    loading: false,
    logo: '/logo.png',
    systemName: 'New API',
  }),
}))
vi.mock('@/features/auth/sign-in/components/user-auth-form', () => ({
  UserAuthForm: () => null,
}))
vi.mock('@/features/auth/components/terms-footer', () => ({
  TermsFooter: () => null,
}))

const { SignIn } = await import('../index')

test('configured login background image covers the sign-in page', () => {
  const markup = renderToStaticMarkup(<SignIn />)

  expect(markup).toContain('bg-cover bg-center')
  expect(markup).not.toContain('bg-background/70')
  expect(markup).toMatch(
    new RegExp(`background-image:url\\(&quot;${backgroundImageUrl}&quot;\\)`)
  )
})

test('sign-in form is shown on a white card', () => {
  const markup = renderToStaticMarkup(<SignIn />)

  expect(markup).toMatch(/class="[^"]*bg-white[^"]*"/)
})
