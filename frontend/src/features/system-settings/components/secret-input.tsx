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
import * as React from 'react'
import { CheckCircle2, Eye, EyeOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

type SecretInputProps = Omit<React.ComponentProps<typeof Input>, 'type'> & {
  configured?: boolean
}

export function SecretInput({
  configured = false,
  className,
  placeholder,
  value,
  ...props
}: SecretInputProps) {
  const { t } = useTranslation()
  const [visible, setVisible] = React.useState(false)
  const hasValue = String(value ?? '').length > 0
  const resolvedPlaceholder =
    configured && !hasValue ? '********' : placeholder || t('Enter new secret')

  return (
    <div className='relative'>
      <Input
        {...props}
        value={value}
        type={visible ? 'text' : 'password'}
        placeholder={resolvedPlaceholder}
        autoComplete={props.autoComplete ?? 'new-password'}
        className={cn('pr-24 font-mono', className)}
      />
      <div className='absolute inset-y-0 right-1 flex items-center gap-1'>
        {configured && !hasValue && (
          <span className='text-muted-foreground inline-flex items-center gap-1 px-1.5 text-xs'>
            <CheckCircle2 className='size-3 text-emerald-600' />
            {t('Configured')}
          </span>
        )}
        <Button
          type='button'
          variant='ghost'
          size='icon-xs'
          title={visible ? t('Hide secret') : t('Show secret')}
          onClick={() => setVisible((next) => !next)}
        >
          {visible ? <EyeOff className='size-3' /> : <Eye className='size-3' />}
        </Button>
      </div>
    </div>
  )
}

type SecretTextareaProps = React.ComponentProps<typeof Textarea> & {
  configured?: boolean
}

export function SecretTextarea({
  configured = false,
  className,
  placeholder,
  value,
  ...props
}: SecretTextareaProps) {
  const { t } = useTranslation()
  const hasValue = String(value ?? '').length > 0
  const resolvedPlaceholder =
    configured && !hasValue ? '********' : placeholder || t('Enter new secret')

  return (
    <div className='relative'>
      <Textarea
        {...props}
        value={value}
        placeholder={resolvedPlaceholder}
        autoComplete={props.autoComplete ?? 'new-password'}
        className={cn('pr-28 font-mono', className)}
      />
      {configured && !hasValue && (
        <span className='text-muted-foreground bg-background absolute top-2 right-2 inline-flex items-center gap-1 rounded px-1.5 text-xs'>
          <CheckCircle2 className='size-3 text-emerald-600' />
          {t('Configured')}
        </span>
      )}
    </div>
  )
}
