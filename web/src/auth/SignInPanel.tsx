import { useState, type FormEvent } from 'react';
import { useAuth } from './AuthProvider';
import { useI18n } from '../i18n';

type Mode = 'signin' | 'register';

// SignInPanel is the console entry point. It supports signing in with an email
// and password, or creating a new account which provisions a personal tenant.
export function SignInPanel() {
  const auth = useAuth();
  const { t } = useI18n();
  const [mode, setMode] = useState<Mode>('signin');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const switchTo = (next: Mode) => {
    setMode(next);
    setPassword('');
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!email.trim() || !password) {
      return;
    }
    setSubmitting(true);
    try {
      if (mode === 'register') {
        await auth.register({ email: email.trim(), password, displayName: displayName.trim() });
      } else {
        await auth.login(email.trim(), password);
      }
    } finally {
      setSubmitting(false);
    }
  };

  const registering = mode === 'register';

  return (
    <form onSubmit={submit} aria-label={registering ? t('signin.registerAria') : t('signin.aria')} className="signin-form">
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

      {registering ? (
        <>
          <label htmlFor="signin-name">{t('signin.displayName')}</label>
          <input
            id="signin-name"
            name="display_name"
            type="text"
            autoComplete="name"
            placeholder={t('signin.displayNamePlaceholder')}
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </>
      ) : null}

      <label htmlFor="signin-password">{t('signin.password')}</label>
      <input
        id="signin-password"
        name="password"
        type="password"
        autoComplete={registering ? 'new-password' : 'current-password'}
        placeholder={t('signin.passwordPlaceholder')}
        value={password}
        onChange={(event) => setPassword(event.target.value)}
      />

      <button type="submit" className="btn btn-primary" disabled={submitting}>
        {submitting ? (registering ? t('signin.creating') : t('signin.signingIn')) : registering ? t('signin.createAccount') : t('signin.signIn')}
      </button>

      <button type="button" className="link" onClick={() => switchTo(registering ? 'signin' : 'register')}>
        {registering ? t('signin.haveAccount') : t('signin.needAccount')}
      </button>

      {auth.status === 'error' && auth.error ? <p role="alert">{auth.error}</p> : null}
    </form>
  );
}
