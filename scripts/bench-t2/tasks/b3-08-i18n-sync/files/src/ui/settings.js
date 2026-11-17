import { t } from '../i18n.js';

export function renderSettings(saved) {
  return {
    title: t('settings.title'),
    language: t('settings.language'),
    theme: t('settings.theme'),
    themes: [t('settings.theme.light'), t('settings.theme.dark')],
    notifications: t('settings.notifications'),
    save: saved ? t('settings.saved') : t('settings.save'),
  };
}
