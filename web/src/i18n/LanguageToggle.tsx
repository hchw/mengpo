import { useI18n } from './index';

export interface LanguageToggleProps {
  className?: string;
}

/**
 * LanguageToggle flips the console between English and Chinese. It shows the
 * language it will switch *to*, so the control reads as an action rather than a
 * current-state label.
 */
export function LanguageToggle({ className }: LanguageToggleProps) {
  const { language, toggle, t } = useI18n();
  const target = language === 'en' ? 'zh' : 'en';
  return (
    <button
      type="button"
      className={className ?? 'btn-ghost lang-toggle'}
      aria-label={t('language.switch')}
      data-testid="language-toggle"
      onClick={toggle}
    >
      <span className="lang-toggle__code">{target === 'zh' ? '中' : 'EN'}</span>
      <span className="lang-toggle__label">{t(`language.${target}`)}</span>
    </button>
  );
}
