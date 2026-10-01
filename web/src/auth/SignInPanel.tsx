import { useState, type FormEvent } from 'react';
import { useAuth } from './AuthProvider';

// SignInPanel is the console entry point. Development builds sign in directly
// with a verified email through the internal SSO exchange; production builds
// hand off to the configured SSO redirect.
export function SignInPanel() {
  const auth = useAuth();
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
    <form onSubmit={submit} aria-label="sign in" className="signin-form">
      <label htmlFor="signin-email">Email</label>
      <input
        id="signin-email"
        name="email"
        type="email"
        autoComplete="email"
        placeholder="you@example.com"
        value={email}
        onChange={(event) => setEmail(event.target.value)}
      />
      <button type="submit" className="btn btn-primary" disabled={submitting}>
        {submitting ? 'Signing in…' : 'Sign in'}
      </button>
      <a className="link" href={auth.loginUrl('/')}>
        Sign in with SSO
      </a>
      {auth.status === 'error' && auth.error ? <p role="alert">{auth.error}</p> : null}
    </form>
  );
}
