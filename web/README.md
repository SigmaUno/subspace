# Subspace owner web app

```sh
pnpm install --frozen-lockfile
SUBSPACE_API_URL=http://localhost:8080 pnpm dev
```

Open http://localhost:3000 and use the Go server's owner token. All owner calls target the Next.js origin, where the route handlers authenticate upstream using the httpOnly session cookie. The production standalone build runs without an API at compile time.

```sh
pnpm lint
pnpm typecheck
pnpm build
```

See [the API contract](../docs/api.md) and [Compose deployment](../deploy/README.md).
