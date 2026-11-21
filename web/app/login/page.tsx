import type { Metadata } from "next";

export const metadata: Metadata = { title: "Sign in" };

const errors = {
  invalid: "Enter a valid token.",
  rejected: "Token rejected.",
  unavailable: "Subspace API is unavailable.",
};

export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const { error } = await searchParams;
  const message = typeof error === "string" && Object.hasOwn(errors, error)
    ? errors[error as keyof typeof errors]
    : null;
  return (
    <main className="login">
      <form action="/session" method="post" className="panel stack">
        <div className="brand"><span className="brand-mark" aria-hidden="true" />Subspace</div>
        <h1>Owner sign-in</h1>
        <label>Owner or API token
          <input name="token" type="password" autoComplete="current-password" autoFocus required />
        </label>
        {message && <p role="alert" className="error">{message}</p>}
        <button type="submit" className="primary">Sign in</button>
      </form>
    </main>
  );
}
