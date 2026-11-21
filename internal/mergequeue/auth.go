package mergequeue

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

const HostForgejo = "forgejo"

type ctxKey int

const (
	credentialKey ctxKey = iota
	gitEnvKey
)

type credential struct {
	env     []string
	secrets []string
}

func (rc RepoConfig) credential() *credential {
	if rc.Token == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + rc.Token))
	header := "Authorization: Basic " + basic
	if rc.Host == HostForgejo {
		header = "Authorization: token " + rc.Token
	}
	return &credential{
		env: []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=" + header,
		},
		secrets: []string{rc.Token, basic},
	}
}

func (rc RepoConfig) missingToken() string {
	if rc.TokenEnv == "" || rc.Token != "" {
		return ""
	}
	return fmt.Sprintf("environment variable %s is not set on the server, so the merge queue cannot authenticate to the git host; set it and restart", rc.TokenEnv)
}

func withCredential(ctx context.Context, rc RepoConfig) context.Context {
	return context.WithValue(ctx, credentialKey, rc.credential())
}

func credentialFrom(ctx context.Context) *credential {
	c, _ := ctx.Value(credentialKey).(*credential)
	return c
}

func gitContext(ctx context.Context) context.Context {
	if c := credentialFrom(ctx); c != nil {
		return context.WithValue(ctx, gitEnvKey, c.env)
	}
	return ctx
}

func envFrom(ctx context.Context) []string {
	env, _ := ctx.Value(gitEnvKey).([]string)
	return env
}

func redact(ctx context.Context, s string) string {
	s = redactCredentials(s)
	if c := credentialFrom(ctx); c != nil {
		for _, secret := range c.secrets {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
