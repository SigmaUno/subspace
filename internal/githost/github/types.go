// Package github implements a GitHub (REST v3) git host for Subspace.
//
// The types in this file are a local copy of what the shared githost package
// is expected to provide; they will be unified later.
package github

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidPRURL     = errors.New("githost: invalid pull request url")
	ErrInvalidBranch    = errors.New("githost: invalid branch name")
	ErrInvalidSignature = errors.New("githost: invalid webhook signature")
	ErrNotFound         = errors.New("githost: not found")
	ErrIgnoredEvent     = errors.New("githost: webhook event ignored")
)

// PRRef identifies a pull request on a host.
type PRRef struct {
	Repo   string // "owner/name"
	Number int
}

// PR is the host-neutral view of a pull request.
type PR struct {
	Ref       PRRef
	URL       string
	Title     string
	Body      string
	State     string // open | closed | merged
	Merged    bool
	Draft     bool
	Branch    string // head branch
	BaseRef   string
	Author    string
	MergedBy  string
	MergedAt  *time.Time
	UpdatedAt time.Time
}

// EventKind is the kind of a normalized webhook event.
type EventKind string

const EventPRMerged EventKind = "pr_merged"

// Event is a normalized webhook event.
type Event struct {
	Kind     EventKind
	Repo     string
	PR       PRRef
	URL      string
	Branch   string
	MergedBy string
	MergedAt *time.Time
}

// Host is the git host interface implemented by this package.
type Host interface {
	GetPR(ctx context.Context, ref PRRef) (*PR, error)
	ParsePRURL(url string) (PRRef, error)
	VerifyWebhook(header map[string][]string, body []byte) error
	ValidateBranch(branch string) error
	ParseWebhook(header map[string][]string, body []byte) (*Event, error)
}
