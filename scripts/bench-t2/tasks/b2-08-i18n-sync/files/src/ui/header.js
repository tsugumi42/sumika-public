import { t } from '../i18n.js';

export function renderHeader() {
  return [t('nav.home'), t('nav.tasks'), t('nav.settings'), t('nav.help')];
}

export function renderBrand() {
  return `${t('app.name')} - ${t('app.tagline')}`;
}
