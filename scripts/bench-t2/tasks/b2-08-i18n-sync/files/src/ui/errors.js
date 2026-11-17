import { t } from '../i18n.js';

const KEYS = {
  network: 'error.network',
  notFound: 'error.notFound',
  permission: 'error.permission',
};

export function errorText(kind) {
  return t(KEYS[kind] || 'error.generic');
}

export function renderHelp() {
  return [t('help.docs'), t('help.contact')];
}

export function renderSearch(query) {
  return {
    placeholder: t('search.placeholder'),
    button: t('search.button'),
    empty: query ? t('empty.search', { query }) : null,
  };
}
