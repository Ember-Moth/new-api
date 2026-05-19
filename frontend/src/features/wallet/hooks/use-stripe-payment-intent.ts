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

import i18next from 'i18next'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'
import { isApiSuccess, requestStripePaymentIntent } from '../api'
import { PAYMENT_TYPES } from '../constants'
import type { StripePaymentIntentResponse } from '../types'

export type StripePaymentIntentData = NonNullable<
  StripePaymentIntentResponse['data']
>

/**
 * Hook for creating Stripe PaymentIntents before rendering Payment Element.
 */
export function useStripePaymentIntent() {
  const [processing, setProcessing] = useState(false)

  const createStripePaymentIntent = useCallback(async (topupAmount: number) => {
    setProcessing(true)

    try {
      const response = await requestStripePaymentIntent({
        amount: Math.floor(topupAmount),
        payment_method: PAYMENT_TYPES.STRIPE_PAYMENT_INTENT,
      })

      if (
        isApiSuccess(response) &&
        response.data?.client_secret &&
        response.data?.publishable_key
      ) {
        return response.data
      }

      toast.error(response.message || i18next.t('Payment request failed'))
      return null
    } catch (_error) {
      toast.error(i18next.t('Payment request failed'))
      return null
    } finally {
      setProcessing(false)
    }
  }, [])

  return { processing, createStripePaymentIntent }
}
