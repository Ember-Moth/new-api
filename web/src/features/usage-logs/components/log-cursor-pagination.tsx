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
import { useTranslation } from 'react-i18next'

import { PageFooterPortal } from '@/components/layout/components/page-footer'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface LogCursorPaginationProps {
  pageIndex: number
  pageSize: number
  hasMore: boolean
  loading: boolean
  onPrevious: () => void
  onNext: () => void
  onPageSizeChange: (size: number) => void
}

export function LogCursorPagination(props: LogCursorPaginationProps) {
  const { t } = useTranslation()
  return (
    <PageFooterPortal>
      <div className='flex items-center justify-end gap-3'>
        <Select
          items={[20, 50, 100].map((size) => ({
            value: String(size),
            label: String(size),
          }))}
          value={String(props.pageSize)}
          onValueChange={(value) => props.onPageSizeChange(Number(value))}
        >
          <SelectTrigger aria-label={t('Rows per page')} className='w-20'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {[20, 50, 100].map((size) => (
                <SelectItem key={size} value={String(size)}>
                  {size}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <span className='text-sm tabular-nums'>
          {t('Page')} {props.pageIndex + 1}
        </span>
        <Button
          variant='outline'
          size='sm'
          disabled={props.loading || props.pageIndex === 0}
          onClick={props.onPrevious}
        >
          {t('Previous page')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={props.loading || !props.hasMore}
          onClick={props.onNext}
        >
          {t('Next page')}
        </Button>
      </div>
    </PageFooterPortal>
  )
}
