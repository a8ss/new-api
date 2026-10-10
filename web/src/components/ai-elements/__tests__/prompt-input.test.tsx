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
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'

import {
  PromptInput,
  PromptInputAttachments,
  PromptInputTextarea,
} from '../prompt-input'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('failed attachment conversion preserves the file and a retry submits it', async () => {
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-attachment')
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  const fetchFile = vi
    .fn()
    .mockRejectedValueOnce(new Error('Attachment unavailable'))
  vi.stubGlobal('fetch', fetchFile)
  const onSubmit = vi.fn()
  render(
    <PromptInput aria-label='Prompt input' onSubmit={onSubmit}>
      <PromptInputAttachments>
        {(file) => <span>{file.filename}</span>}
      </PromptInputAttachments>
      <PromptInputTextarea />
    </PromptInput>
  )
  fireEvent.change(screen.getByLabelText('Upload files'), {
    target: {
      files: [new File(['image'], 'sample.png', { type: 'image/png' })],
    },
  })
  await act(async () => {
    fireEvent.submit(screen.getByRole('form', { name: 'Prompt input' }))
  })
  expect(fetchFile).toHaveBeenCalledWith('blob:test-attachment')
  expect(onSubmit).not.toHaveBeenCalled()
  expect(screen.getByText('sample.png')).toBeInTheDocument()

  fetchFile.mockResolvedValueOnce({
    blob: async () => new Blob(['image'], { type: 'image/png' }),
  })
  fireEvent.submit(screen.getByRole('form', { name: 'Prompt input' }))
  await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce())
  expect(onSubmit.mock.calls[0][0].files[0].url).toBe(
    'data:image/png;base64,aW1hZ2U='
  )
  await waitFor(() =>
    expect(screen.queryByText('sample.png')).not.toBeInTheDocument()
  )
})
