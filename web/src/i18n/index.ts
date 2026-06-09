import { createI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'

const STORAGE_KEY = 'pp.lang'

export type Lang = 'en' | 'zh-CN'

function detectInitialLang(): Lang {
  if (typeof localStorage !== 'undefined') {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved === 'en' || saved === 'zh-CN') return saved
  }
  return 'en'
}

export const i18n = createI18n({
  legacy: false, // use Composition API style
  globalInjection: true,
  locale: detectInitialLang(),
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN },
})

export function setLang(l: Lang) {
  i18n.global.locale.value = l
  try { localStorage.setItem(STORAGE_KEY, l) } catch {}
}

export function currentLang(): Lang {
  return i18n.global.locale.value as Lang
}

export const SUPPORTED_LANGS: { value: Lang; label: string }[] = [
  { value: 'en', label: 'English' },
  { value: 'zh-CN', label: '简体中文' },
]
