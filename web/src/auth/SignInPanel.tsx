import { useState, type FormEvent } from 'react';
import { useAuth } from './AuthProvider';
import { useI18n } from '../i18n';

// SignInPanel is the console entry point. Development builds sign in directly
// with a verified email through the internal SSO exchange; production builds
// hand off to the configured SSO redirect.
export function SignInPanel() {
  const auth = useAuth();
  const { t } = useI18n();
  const [email, setEmail] = useState('dev@mengpo.local');
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!email.trim()) {
      return;
    }
    setSubmitting(true);
    try {
      await auth.handleSsoCallback(`/?code=${encodeURIComponent(email.trim())}&state=dev-local`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={submit} aria-label={t('signin.aria')} className="signin-form">
      <label htmlFor="signin-email">{t('signin.email')}</label>
      <input
        id="signin-email"
        name="email"
        type="email"
        autoComplete="email"
        placeholder={t('signin.placeholder')}
        value={email}
        onChange={(event) => setEmail(event.target.value)}
      />
      <button type="submit" className="btn btn-primary" disabled={submitting}>
        {submitting ? t('signin.signingIn') : t('signin.signIn')}
      </button>
      <a className="link" href={auth.loginUrl('/')}>
        {t('signin.withSso')}
      </a>
      {auth.status === 'error' && auth.error ? <p role="alert">{auth.error}</p> : null}
    </form>
  );
}
