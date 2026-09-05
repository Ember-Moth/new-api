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
import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { PageFooterProvider } from '@/components/layout/components/page-footer'

import { LogCursorPagination } from '../log-cursor-pagination'

describe('log cursor navigation', () => {
  let footer: HTMLDivElement
  beforeEach(() => {
    footer = document.createElement('div')
    document.body.appendChild(footer)
  })
  afterEach(() => footer.remove())
  it('disables unavailable directions and navigates only with a next cursor', () => {
    const next = vi.fn()
    const previous = vi.fn()
    const props = {
      pageIndex: 0,
      pageSize: 20,
      hasMore: true,
      loading: false,
      onNext: next,
      onPrevious: previous,
      onPageSizeChange: vi.fn(),
    }
    const view = render(
      <PageFooterProvider container={footer}>
        <LogCursorPagination {...props} />
      </PageFooterProvider>
    )
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(next).toHaveBeenCalledOnce()
    view.rerender(
      <PageFooterProvider container={footer}>
        <LogCursorPagination {...props} pageIndex={1} hasMore={false} />
      </PageFooterProvider>
    )
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Previous page' }))
    expect(previous).toHaveBeenCalledOnce()
  })

  it('prevents duplicate navigation while the next page is loading', () => {
    render(
      <PageFooterProvider container={footer}>
        <LogCursorPagination
          pageIndex={1}
          pageSize={20}
          hasMore
          loading
          onNext={vi.fn()}
          onPrevious={vi.fn()}
          onPageSizeChange={vi.fn()}
        />
      </PageFooterProvider>
    )
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  })
})
