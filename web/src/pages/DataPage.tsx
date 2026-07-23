import { useLanguage } from '../contexts/LanguageContext'
import { t } from '../i18n/translations'

const VERGEX_TRENDING_URL = 'https://vergex.trade/trending'

// vergex.trade's CSP (frame-ancestors) only allows nofxos.ai and localhost
// to embed it, so an iframe renders blank on self-hosted domains. Link out
// to a new tab instead.
export function DataPage() {
  const { language } = useLanguage()

  return (
    <div className="flex h-[calc(100vh-64px)] w-full items-center justify-center">
      <div className="max-w-md rounded-xl border border-[rgba(26,24,19,0.14)] bg-nofx-bg p-8 text-center">
        <h1 className="text-lg font-semibold text-nofx-text">
          {t('dataCenter', language)}
        </h1>
        <p className="mt-3 text-sm text-nofx-text-muted">
          Vergex market data can&apos;t be embedded on self-hosted domains, so
          it opens in a new tab.
        </p>
        <a
          href={VERGEX_TRENDING_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-6 inline-block rounded-lg bg-nofx-text px-5 py-2.5 text-sm font-medium text-nofx-bg transition-opacity hover:opacity-80"
        >
          Open Vergex Trending ↗
        </a>
      </div>
    </div>
  )
}
