import { beforeAll, afterEach } from 'vitest'
import i18n from './index'

// Force tests to run in zh-CN regardless of jsdom's navigator.language
// default ('en-US'). Existing Chinese-text assertions depend on this.
beforeAll(async () => {
  localStorage.clear()
  await i18n.changeLanguage('zh-CN')
})

afterEach(() => {
  localStorage.clear()
})
