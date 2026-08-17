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
import assert from 'node:assert/strict'

import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const bunTestModule = 'bun:test'
const { mock, test } = (await import(bunTestModule)) as {
  mock: {
    module: (specifier: string, factory: () => object) => void
  }
  test: typeof import('node:test').test
}

const backgroundImageUrl = 'https://example.com/login-background.jpg'
process.env.VITE_LOGIN_BACKGROUND_IMAGE_URL = backgroundImageUrl

mock.module('@tanstack/react-router', () => ({
  Link: (props: { children?: ReactNode }) => <a>{props.children}</a>,
  useSearch: () => ({}),
}))
mock.module('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))
mock.module('@/hooks/use-status', () => ({
  useStatus: () => ({ status: { register_enabled: false } }),
}))
mock.module('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    loading: false,
    logo: '/logo.png',
    systemName: 'New API',
  }),
}))
mock.module('@/features/auth/sign-in/components/user-auth-form', () => ({
  UserAuthForm: () => null,
}))
mock.module('@/features/auth/components/terms-footer', () => ({
  TermsFooter: () => null,
}))

const { SignIn } = await import('../index')

test('configured login background image covers the sign-in page', () => {
  const markup = renderToStaticMarkup(<SignIn />)

  assert.match(
    markup,
    /class="relative grid h-svh max-w-none bg-cover bg-center"/
  )
  assert.doesNotMatch(markup, /bg-background\/70/)
  assert.match(
    markup,
    new RegExp(`background-image:url\\(&quot;${backgroundImageUrl}&quot;\\)`)
  )
})

test('sign-in form is shown on a white card', () => {
  const markup = renderToStaticMarkup(<SignIn />)

  assert.match(
    markup,
    /class="w-full space-y-8 rounded-2xl bg-white p-6 shadow-xl sm:p-8"/
  )
})
