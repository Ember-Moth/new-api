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
  Elements,
  PaymentElement,
  useElements,
  useStripe,
} from '@stripe/react-stripe-js'
import { loadStripe } from '@stripe/stripe-js'
import { CreditCard, Loader2 } from 'lucide-react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export type StripePaymentIntentData = {
  client_secret: string
  publishable_key: string
  trade_no: string
  payment_intent_id: string
  amount: number
  currency: string
}

type StripePaymentIntentDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  paymentIntent: StripePaymentIntentData | null
  onPaymentSettled?: () => Promise<void> | void
  returnUrl?: string
  successMessage?: string
}

const ZERO_DECIMAL_CURRENCIES = new Set([
  'bif',
  'clp',
  'djf',
  'gnf',
  'jpy',
  'kmf',
  'krw',
  'mga',
  'pyg',
  'rwf',
  'ugx',
  'vnd',
  'vuv',
  'xaf',
  'xof',
  'xpf',
])

function formatStripeAmount(amount: number, currency: string) {
  const normalizedCurrency = currency.trim().toLowerCase()
  const majorAmount = ZERO_DECIMAL_CURRENCIES.has(normalizedCurrency)
    ? amount
    : amount / 100

  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: normalizedCurrency.toUpperCase(),
    }).format(majorAmount)
  } catch {
    return `${normalizedCurrency.toUpperCase()} ${majorAmount.toFixed(2)}`
  }
}

function StripePaymentIntentForm({
  paymentIntent,
  onOpenChange,
  onPaymentSettled,
  returnUrl,
  successMessage,
}: {
  paymentIntent: StripePaymentIntentData
  onOpenChange: (open: boolean) => void
  onPaymentSettled?: () => Promise<void> | void
  returnUrl?: string
  successMessage?: string
}) {
  const { t } = useTranslation()
  const stripe = useStripe()
  const elements = useElements()
  const [submitting, setSubmitting] = React.useState(false)

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!stripe) {
      toast.error(t('Stripe is still loading. Please try again.'))
      return
    }
    if (!elements) {
      toast.error(t('Payment form is not ready. Please try again.'))
      return
    }

    setSubmitting(true)
    const result = await stripe.confirmPayment({
      elements,
      confirmParams: {
        return_url:
          returnUrl || `${window.location.origin}/wallet?show_history=true`,
      },
      redirect: 'if_required',
    })
    setSubmitting(false)

    if (result.error) {
      toast.error(result.error.message || t('Payment request failed'))
      return
    }

    toast.success(
      successMessage ||
        t('Payment submitted. Balance will update after confirmation.')
    )
    onOpenChange(false)
    await onPaymentSettled?.()
  }

  return (
    <form className='space-y-4' onSubmit={handleSubmit}>
      <div className='bg-muted/40 flex items-center justify-between rounded-lg border px-3 py-2.5'>
        <span className='text-muted-foreground text-sm'>{t('You Pay')}</span>
        <span className='font-semibold'>
          {formatStripeAmount(paymentIntent.amount, paymentIntent.currency)}
        </span>
      </div>

      <PaymentElement />

      <DialogFooter>
        <Button
          type='button'
          variant='outline'
          disabled={submitting}
          onClick={() => onOpenChange(false)}
        >
          {t('Cancel')}
        </Button>
        <Button type='submit' disabled={submitting || !stripe || !elements}>
          {submitting && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
          {t('Pay now')}
        </Button>
      </DialogFooter>
    </form>
  )
}

export function StripePaymentIntentDialog({
  open,
  onOpenChange,
  paymentIntent,
  onPaymentSettled,
  returnUrl,
  successMessage,
}: StripePaymentIntentDialogProps) {
  const { t } = useTranslation()
  const publishableKey = paymentIntent?.publishable_key
  const stripePromise = React.useMemo(() => {
    if (!publishableKey) {
      return null
    }
    return loadStripe(publishableKey)
  }, [publishableKey])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle className='flex items-center gap-2 text-lg'>
            <CreditCard className='h-4 w-4' />
            {t('Complete payment')}
          </DialogTitle>
          <DialogDescription>
            {t('Payment is processed securely by Stripe.')}
          </DialogDescription>
        </DialogHeader>

        {paymentIntent && stripePromise ? (
          <Elements
            key={paymentIntent.client_secret}
            stripe={stripePromise}
            options={{
              clientSecret: paymentIntent.client_secret,
              appearance: { theme: 'stripe' },
            }}
          >
            <StripePaymentIntentForm
              paymentIntent={paymentIntent}
              onOpenChange={onOpenChange}
              onPaymentSettled={onPaymentSettled}
              returnUrl={returnUrl}
              successMessage={successMessage}
            />
          </Elements>
        ) : (
          <div className='text-muted-foreground rounded-lg border p-4 text-sm'>
            {t('Unable to load payment form. Please close and try again.')}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
