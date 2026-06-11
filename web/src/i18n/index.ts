import { createI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'

const LANG_KEY = 'pp.lang'
const THEME_KEY = 'pp.theme'

export type Lang = 'en' | 'zh-CN'
export type Theme = 'dark' | 'light'

function detectInitialLang(): Lang {
  if (typeof localStorage !== 'undefined') {
    const saved = localStorage.getItem(LANG_KEY)
    if (saved === 'en' || saved === 'zh-CN') return saved
  }
  return 'en'
}

function detectInitialTheme(): Theme {
  if (typeof localStorage !== 'undefined') {
    const saved = localStorage.getItem(THEME_KEY)
    if (saved === 'dark' || saved === 'light') return saved
  }
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  }
  return 'dark'
}

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: detectInitialLang(),
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN },
})

export function setLang(l: Lang) {
  i18n.global.locale.value = l
  try { localStorage.setItem(LANG_KEY, l) } catch {}
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('lang', l === 'zh-CN' ? 'zh-CN' : 'en')
  }
}

export function currentLang(): Lang {
  return i18n.global.locale.value as Lang
}

export const SUPPORTED_LANGS: { value: Lang; label: string }[] = [
  { value: 'en', label: 'English' },
  { value: 'zh-CN', label: '简体中文' },
]

/* ----- Theme ----- */
export const theme = {
  current: detectInitialTheme() as Theme,
  set(t: Theme) {
    theme.current = t
    if (typeof document !== 'undefined') {
      document.documentElement.setAttribute('data-theme', t)
    }
    try { localStorage.setItem(THEME_KEY, t) } catch {}
  },
  toggle() {
    theme.set(theme.current === 'dark' ? 'light' : 'dark')
  },
}

// Apply initial theme on module load so the very first paint matches.
if (typeof document !== 'undefined') {
  document.documentElement.setAttribute('data-theme', theme.current)
  document.documentElement.setAttribute('lang', currentLang() === 'zh-CN' ? 'zh-CN' : 'en')
}
