package scope

import (
	"path"
	"slices"
	"strings"
)

var (
	protectedDirs = []string{".github", ".gitea", ".forgejo", ".circleci", ".buildkite", "deploy", "deployment", "k8s", "helm", "terraform"}

	ciFiles = []string{".gitlab-ci.yml", ".travis.yml", "jenkinsfile", "azure-pipelines.yml", "bitbucket-pipelines.yml", "cloudbuild.yaml", "cloudbuild.yml"}

	deployFiles = []string{"caddyfile", "dockerfile", "compose.yml", "compose.yaml", "fly.toml", "netlify.toml", "vercel.json", "procfile"}

	lockFiles = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "go.sum", "cargo.lock",
		"poetry.lock", "pipfile.lock", "gemfile.lock", "composer.lock", "uv.lock"}

	secretExts = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".secret", ".secrets"}

	secretNames = []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".netrc", ".npmrc", ".pgpass"}
)

func protectedReason(p string) string {
	parts := strings.Split(strings.ToLower(p), "/")
	if len(parts) > 1 && slices.Contains(protectedDirs, parts[0]) {
		return "deploy"
	}
	base := parts[len(parts)-1]
	switch {
	case slices.Contains(ciFiles, base):
		return "ci"
	case slices.Contains(lockFiles, base) || path.Ext(base) == ".lock":
		return "lockfile"
	case slices.Contains(deployFiles, base) || strings.HasPrefix(base, "dockerfile.") || strings.HasSuffix(base, ".dockerfile") ||
		strings.HasPrefix(base, "docker-compose"):
		return "deploy"
	case isSecretName(base):
		return "secret"
	}
	return ""
}

func isSecretName(base string) bool {
	switch {
	case slices.Contains(secretNames, base), slices.Contains(secretExts, path.Ext(base)):
		return true
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") && !strings.HasSuffix(base, ".template")
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "credential")
}
