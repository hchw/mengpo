import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { LanguageProvider, useI18n } from './index';
import { LanguageToggle } from './LanguageToggle';

function Probe() {
  const { t, language } = useI18n();
  return (
    <div>
      <span data-testid="language">{language}</span>
      <span data-testid="title">{t('dashboard.title')}</span>
      <span data-testid="interpolated">{t('pagination.status', { page: 2, totalPages: 5, total: 100 })}</span>
      <LanguageToggle />
    </div>
  );
}

describe('i18n', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('defaults to English and falls back to it without a provider', () => {
    render(<Probe />);
    expect(screen.getByTestId('language').textContent).toBe('en');
    expect(screen.getByTestId('title').textContent).toBe('Dashboard');
  });

  it('switches to Chinese (and back) through the toggle', async () => {
    const user = userEvent.setup();
    render(
      <LanguageProvider initialLanguage="en">
        <Probe />
      </LanguageProvider>,
    );
    expect(screen.getByTestId('title').textContent).toBe('Dashboard');
    expect(screen.getByTestId('interpolated').textContent).toBe('Page 2 of 5 · 100 total');
    // The toggle advertises the language it will switch to.
    expect(screen.getByTestId('language-toggle').textContent).toContain('中文');

    await user.click(screen.getByTestId('language-toggle'));

    expect(screen.getByTestId('language').textContent).toBe('zh');
    expect(screen.getByTestId('title').textContent).toBe('仪表盘');
    expect(screen.getByTestId('interpolated').textContent).toBe('第 2 / 5 页 · 共 100 条');
    expect(document.documentElement.lang).toBe('zh-CN');
    expect(window.localStorage.getItem('mengpo.language')).toBe('zh');

    await user.click(screen.getByTestId('language-toggle'));
    expect(screen.getByTestId('title').textContent).toBe('Dashboard');
  });

  it('restores a persisted language preference', () => {
    window.localStorage.setItem('mengpo.language', 'zh');
    render(
      <LanguageProvider>
        <Probe />
      </LanguageProvider>,
    );
    expect(screen.getByTestId('language').textContent).toBe('zh');
    expect(screen.getByTestId('title').textContent).toBe('仪表盘');
  });
});
