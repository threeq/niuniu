import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import * as Localization from 'expo-localization';

import zhCN from './resources/zh-CN/common.json';
import enUS from './resources/en-US/common.json';

const locale = Localization.getLocales?.()[0]?.languageTag ?? 'zh-CN';
const langCode = locale.startsWith('zh') ? 'zh-CN' : 'en-US';

i18n.use(initReactI18next).init({
  resources: {
    'zh-CN': { translation: zhCN },
    'en-US': { translation: enUS },
  },
  lng: langCode,
  fallbackLng: 'zh-CN',
  interpolation: { escapeValue: false },
});

export default i18n;
