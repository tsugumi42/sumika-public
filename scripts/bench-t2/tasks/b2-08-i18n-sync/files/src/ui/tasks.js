import { t } from '../i18n.js';

export function renderTaskForm() {
  return {
    create: t('task.create'),
    title: t('task.title'),
    placeholder: t('task.titlePlaceholder'),
    tags: t('task.tags'),
  };
}

export function renderTaskRow(task) {
  return {
    status: t(`task.status.${task.status}`),
    retry: task.status === 'failed' ? t('task.retry') : null,
    copyLink: t('task.copyLink'),
    confirmDelete: t('task.deleteConfirm', { name: task.name }),
  };
}

export function renderCount(n) {
  return n === 0 ? t('empty.tasks') : t('task.count', { n });
}
