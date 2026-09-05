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
import { describe, expect, it } from 'vitest'

import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import { aggregateChannelsByTag } from '../channel-utils'

describe('channel array payloads', () => {
  it('creates and updates channels with arrays for models and groups', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'native arrays',
      key: 'fixture',
      models: ' model-a,model-b ',
      group: ['default', 'vip%'],
    }
    const created = transformFormDataToCreatePayload(form).channel
    const updated = transformFormDataToUpdatePayload(form, 1)
    expect(created.models).toEqual(['model-a', 'model-b'])
    expect(created.group).toEqual(['default', 'vip%'])
    expect(updated.models).toEqual(created.models)
    expect(updated.group).toEqual(created.group)
  })

  it('loads array responses into editable form values without changing group names', () => {
    const values = transformChannelToFormDefaults(
      channelSchema.parse({
        id: 1,
        name: 'array response',
        key: 'fixture',
        type: 1,
        status: 1,
        created_time: 0,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
        models: ['model-a', 'model-b'],
        group: ['default', 'vip,experimental'],
      })
    )
    expect(values.models).toBe('model-a,model-b')
    expect(values.group).toEqual(['default', 'vip,experimental'])
  })

  it('aggregates literal group names across channels without mutating source arrays', () => {
    const base = {
      id: 1,
      name: 'one',
      key: 'fixture',
      type: 1,
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      tag: 'same',
      models: ['model'],
      group: ['default', 'vip,experimental'],
    }
    const first = channelSchema.parse(base)
    const second = channelSchema.parse({
      ...base,
      id: 2,
      group: ['vip,experimental', 'other'],
    })
    const grouped = aggregateChannelsByTag([first, second])
    expect(grouped[0].group).toEqual(['default', 'vip,experimental', 'other'])
    expect(first.group).toEqual(['default', 'vip,experimental'])
  })
})
