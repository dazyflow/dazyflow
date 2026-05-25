import { useState } from "react";
import { useAuth } from "../auth";

export function SignIn() {
  const { signIn, error, loading } = useAuth();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!token.trim()) return;
          setBusy(true);
          try {
            await signIn(token.trim());
          } catch {
            /* error already set on context */
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>Sign in</h1>
        <label htmlFor="apikey">API key</label>
        <input
          id="apikey"
          type="password"
          autoComplete="off"
          autoFocus
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="hzd-…"
        />
        <button
          type="submit"
          className="primary"
          disabled={busy || loading || !token.trim()}
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
        {error && <div className="error">{error}</div>}
        <div className="hint">
          Generate a key on the daemon with{" "}
          <code>hzctl api-key create</code>.
        </div>
      </form>
    </div>
  );
}
