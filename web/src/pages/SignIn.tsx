import { useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth";

export function SignIn() {
  const { signInWithPassword, error, loading } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim() || !password) return;
          setBusy(true);
          try {
            await signInWithPassword(email.trim(), password);
          } catch {
            /* error already set on context */
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>Sign in</h1>
        <label htmlFor="email">Email</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <label htmlFor="password">Password</label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button
          type="submit"
          className="primary"
          disabled={busy || loading || !email.trim() || !password}
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
        {error && <div className="error">{error}</div>}
        <div className="signin-alt">
          New here? <Link to="/signup">Create an account</Link>
        </div>
      </form>
    </div>
  );
}
