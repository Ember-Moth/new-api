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
import { Database, Server } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import type { SetupStatus } from '../types'

interface DatabaseStepProps {
  status?: SetupStatus
}

export function DatabaseStep(props: DatabaseStepProps) {
  const { t } = useTranslation()
  const isPostgreSQL = props.status?.database_type?.toLowerCase() === 'postgres'
  const label = isPostgreSQL ? 'PostgreSQL' : t('Unknown')

  return (
    <div className='space-y-4'>
      <div className='bg-card flex items-center justify-between rounded-lg border p-4'>
        <div className='space-y-1'>
          <p className='text-muted-foreground text-sm font-medium'>
            {t('Detected database')}
          </p>
          <p className='text-foreground text-base font-semibold'>{label}</p>
          <p className='text-muted-foreground text-sm'>
            {t(
              isPostgreSQL
                ? 'PostgreSQL offers advanced reliability and data integrity for production workloads.'
                : 'The setup wizard will use this database during initialization.'
            )}
          </p>
        </div>
        <StatusBadge
          label={label}
          variant={isPostgreSQL ? 'success' : 'info'}
          className='cursor-default'
          copyable={false}
          icon={Database}
        />
      </div>

      {isPostgreSQL && (
        <Alert className='border-sky-200 bg-sky-50 dark:border-sky-900/60 dark:bg-sky-950/40'>
          <AlertTitle className='flex items-center gap-2'>
            <Server className='size-4 text-sky-500' />
            {t('PostgreSQL detected')}
          </AlertTitle>
          <AlertDescription>
            {t(
              'PostgreSQL offers strong reliability guarantees. Double check your maintenance window and retention policies before going live.'
            )}
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
