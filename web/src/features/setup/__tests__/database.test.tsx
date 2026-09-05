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
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { DatabaseStep } from '../components/database-step'

describe('setup database', () => {
  it('shows PostgreSQL guidance when PostgreSQL is connected', () => {
    render(
      <DatabaseStep
        status={{ status: false, root_init: false, database_type: 'postgres' }}
      />
    )
    expect(screen.getByText('PostgreSQL detected')).toBeVisible()
  })

  it.each(['sqlite', 'mysql'])('does not advertise %s as supported', (type) => {
    render(
      <DatabaseStep
        status={{ status: false, root_init: false, database_type: type }}
      />
    )
    expect(screen.queryByText('Persist your data file')).not.toBeInTheDocument()
    expect(screen.queryByText('MySQL detected')).not.toBeInTheDocument()
    expect(screen.getAllByText('Unknown').length).toBeGreaterThan(0)
  })

  it('shows an unknown database while setup status is unavailable', () => {
    render(<DatabaseStep />)
    expect(screen.getAllByText('Unknown').length).toBeGreaterThan(0)
    expect(screen.queryByText('PostgreSQL detected')).not.toBeInTheDocument()
  })
})
