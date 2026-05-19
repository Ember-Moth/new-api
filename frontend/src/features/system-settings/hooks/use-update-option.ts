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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import i18next from 'i18next'
import { toast } from 'sonner'
import { applyBrandToDom, patchCachedStatus } from '@/lib/brand-bootstrap'
import { DEFAULT_LOGO, DEFAULT_SYSTEM_NAME } from '@/lib/constants'
import { useSystemConfigStore } from '@/stores/system-config-store'
import { updateSystemOption } from '../api'
import type { SystemOptionsResponse, UpdateOptionRequest } from '../types'

// Configuration keys that require status refresh
const STATUS_RELATED_KEYS = [
  'SystemName',
  'Logo',
  'Footer',
  'ServerAddress',
  'HeaderNavModules',
  'SidebarModulesAdmin',
  'Notice',
  'LogConsumeEnabled',
  'QuotaPerUnit',
  'USDExchangeRate',
  'DisplayInCurrencyEnabled',
  'DisplayTokenStatEnabled',
  'general_setting.quota_display_type',
  'general_setting.custom_currency_symbol',
  'general_setting.custom_currency_exchange_rate',
]

const SENSITIVE_OPTION_KEYS = new Set([
  'GitHubClientSecret',
  'discord.client_secret',
  'oidc.client_secret',
  'TelegramBotToken',
  'LinuxDOClientSecret',
  'WeChatServerToken',
  'TurnstileSecretKey',
  'SMTPToken',
  'WorkerValidKey',
  'EpayKey',
  'StripeApiSecret',
  'StripeWebhookSecret',
  'StripePaymentIntentApiSecret',
  'StripePaymentIntentWebhookSecret',
  'CreemApiKey',
  'CreemWebhookSecret',
  'WaffoApiKey',
  'WaffoPrivateKey',
  'WaffoSandboxApiKey',
  'WaffoSandboxPrivateKey',
  'WaffoPancakePrivateKey',
  'model_deployment.ionet.api_key',
])

function syncBrandOptionToLocalState(
  key: string,
  value: string | boolean | number
) {
  const { setConfig } = useSystemConfigStore.getState()

  if (key === 'SystemName') {
    const systemName = String(value).trim() || DEFAULT_SYSTEM_NAME
    setConfig({ systemName })
    applyBrandToDom({ systemName })
    patchCachedStatus({ system_name: systemName })
    return
  }

  if (key === 'Logo') {
    const logo = String(value).trim() || DEFAULT_LOGO
    setConfig({ logo })
    applyBrandToDom({ logo })
    patchCachedStatus({ logo })
    return
  }

  if (key === 'Footer') {
    setConfig({ footerHtml: String(value) })
    patchCachedStatus({ footer_html: String(value) })
    return
  }

  if (key === 'ServerAddress') {
    patchCachedStatus({ server_address: String(value).trim() })
  }
}

function patchSensitiveOptionConfiguredState(
  queryClient: ReturnType<typeof useQueryClient>,
  key: string,
  value: string | boolean | number
) {
  if (!SENSITIVE_OPTION_KEYS.has(key)) return

  const configuredKey = `${key}_configured`
  const configuredValue = String(
    typeof value === 'string' ? value.trim() !== '' : Boolean(value)
  )

  queryClient.setQueryData<SystemOptionsResponse>(
    ['system-options'],
    (current) => {
      if (!current?.data) return current

      let hasConfiguredKey = false
      const data = current.data.flatMap((option) => {
        if (option.key === key) return []
        if (option.key === configuredKey) {
          hasConfiguredKey = true
          return [{ ...option, value: configuredValue }]
        }
        return [option]
      })

      if (!hasConfiguredKey) {
        data.push({ key: configuredKey, value: configuredValue })
      }

      return { ...current, data }
    }
  )
}

export function useUpdateOption() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (request: UpdateOptionRequest) => updateSystemOption(request),
    onSuccess: (data, variables) => {
      if (data.success) {
        syncBrandOptionToLocalState(variables.key, variables.value)
        patchSensitiveOptionConfiguredState(
          queryClient,
          variables.key,
          variables.value
        )

        // Always refresh system-options
        queryClient.invalidateQueries({ queryKey: ['system-options'] })

        // If updating frontend-display-related config, also refresh status
        if (STATUS_RELATED_KEYS.includes(variables.key)) {
          queryClient.invalidateQueries({ queryKey: ['status'] })
        }

        toast.success(i18next.t('Setting updated successfully'))
      } else {
        toast.error(data.message || i18next.t('Failed to update setting'))
      }
    },
    onError: (error: Error) => {
      toast.error(error.message || i18next.t('Failed to update setting'))
    },
  })
}
