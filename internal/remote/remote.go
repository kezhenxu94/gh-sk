// Package remote talks to the GitHub REST API for Actions secrets and variables.
package remote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
	"golang.org/x/crypto/nacl/box"

	"github.com/kezhenxu94/github-secrets-keychain/internal/scope"
	"github.com/kezhenxu94/github-secrets-keychain/internal/store"
)

// Entry is what GitHub reports for one secret or variable. Value is empty for secrets.
type Entry struct {
	Value     string
	UpdatedAt time.Time
}

type Remote interface {
	ListSecrets(ctx context.Context, s scope.Scope) (map[string]Entry, error)
	PutSecret(ctx context.Context, s scope.Scope, name, value string, meta store.Meta) error
	DeleteSecret(ctx context.Context, s scope.Scope, name string) error
	ListVars(ctx context.Context, s scope.Scope) (map[string]Entry, error)
	PutVar(ctx context.Context, s scope.Scope, name, value string, exists bool, meta store.Meta) error
	DeleteVar(ctx context.Context, s scope.Scope, name string) error
}

type Client struct {
	gh *github.Client
}

// Token returns GITHUB_TOKEN / GH_TOKEN, falling back to `gh auth token`.
func Token() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := os.Getenv(k); t != "" {
			return t, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", errors.New("no GitHub token: set GITHUB_TOKEN or run `gh auth login`")
	}
	return strings.TrimSpace(string(out)), nil
}

func New() (*Client, error) {
	tok, err := Token()
	if err != nil {
		return nil, err
	}
	gh, err := github.NewClient(github.WithAuthToken(tok), github.WithUserAgent("ghsk"))
	if err != nil {
		return nil, err
	}
	return &Client{gh: gh}, nil
}

func wrap(err error, s scope.Scope) error {
	var er *github.ErrorResponse
	if errors.As(err, &er) && (er.Response.StatusCode == http.StatusForbidden || er.Response.StatusCode == http.StatusNotFound) {
		need := "repo"
		if s.IsOrg() {
			need = "admin:org"
		}
		return fmt.Errorf("%s: %w (check the scope exists and the token has the %q scope)", s, err, need)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", s, err)
	}
	return nil
}

func (c *Client) ListSecrets(ctx context.Context, s scope.Scope) (map[string]Entry, error) {
	names := map[string]Entry{}
	opts := &github.ListOptions{PerPage: 100}
	for {
		var res *github.Secrets
		var resp *github.Response
		var err error
		switch {
		case s.IsOrg():
			res, resp, err = c.gh.Actions.ListOrgSecrets(ctx, s.Org, opts)
		case s.IsEnv():
			res, resp, err = c.gh.Actions.ListEnvSecrets(ctx, s.Org, s.Repo, s.Env, opts)
		default:
			res, resp, err = c.gh.Actions.ListRepoSecrets(ctx, s.Org, s.Repo, opts)
		}
		if err != nil {
			return nil, wrap(err, s)
		}
		for _, sec := range res.Secrets {
			names[sec.Name] = Entry{UpdatedAt: sec.UpdatedAt.Time}
		}
		if resp.NextPage == 0 {
			return names, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) publicKey(ctx context.Context, s scope.Scope) (*github.PublicKey, error) {
	var pk *github.PublicKey
	var err error
	switch {
	case s.IsOrg():
		pk, _, err = c.gh.Actions.GetOrgPublicKey(ctx, s.Org)
	case s.IsEnv():
		pk, _, err = c.gh.Actions.GetEnvPublicKey(ctx, s.Org, s.Repo, s.Env)
	default:
		pk, _, err = c.gh.Actions.GetRepoPublicKey(ctx, s.Org, s.Repo)
	}
	return pk, wrap(err, s)
}

// seal encrypts value with a libsodium sealed box, as required by the secrets API.
func seal(pk *github.PublicKey, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(pk.GetKey())
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("invalid public key from GitHub")
	}
	var key [32]byte
	copy(key[:], raw)
	enc, err := box.SealAnonymous(nil, []byte(value), &key, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func (c *Client) repoIDs(ctx context.Context, org string, repos []string) ([]int64, error) {
	ids := make([]int64, 0, len(repos))
	for _, r := range repos {
		repo, _, err := c.gh.Repositories.Get(ctx, org, r)
		if err != nil {
			return nil, fmt.Errorf("resolve repo %s/%s: %w", org, r, err)
		}
		ids = append(ids, repo.GetID())
	}
	return ids, nil
}

func visibility(m store.Meta) string {
	if m.Visibility == "" {
		return "private"
	}
	return m.Visibility
}

func (c *Client) PutSecret(ctx context.Context, s scope.Scope, name, value string, meta store.Meta) error {
	pk, err := c.publicKey(ctx, s)
	if err != nil {
		return err
	}
	enc, err := seal(pk, value)
	if err != nil {
		return err
	}
	switch {
	case s.IsOrg():
		req := github.SecretOrgRequest{KeyID: pk.GetKeyID(), EncryptedValue: enc, Visibility: visibility(meta)}
		if req.Visibility == "selected" {
			if req.SelectedRepositoryIDs, err = c.repoIDs(ctx, s.Org, meta.Repos); err != nil {
				return err
			}
		}
		_, err = c.gh.Actions.CreateOrUpdateOrgSecret(ctx, s.Org, name, req)
	case s.IsEnv():
		_, err = c.gh.Actions.CreateOrUpdateEnvSecret(ctx, s.Org, s.Repo, s.Env, name, github.SecretRequest{KeyID: pk.GetKeyID(), EncryptedValue: enc})
	default:
		_, err = c.gh.Actions.CreateOrUpdateRepoSecret(ctx, s.Org, s.Repo, name, github.SecretRequest{KeyID: pk.GetKeyID(), EncryptedValue: enc})
	}
	return wrap(err, s)
}

func (c *Client) DeleteSecret(ctx context.Context, s scope.Scope, name string) error {
	var err error
	switch {
	case s.IsOrg():
		_, err = c.gh.Actions.DeleteOrgSecret(ctx, s.Org, name)
	case s.IsEnv():
		_, err = c.gh.Actions.DeleteEnvSecret(ctx, s.Org, s.Repo, s.Env, name)
	default:
		_, err = c.gh.Actions.DeleteRepoSecret(ctx, s.Org, s.Repo, name)
	}
	return wrap(err, s)
}

func (c *Client) ListVars(ctx context.Context, s scope.Scope) (map[string]Entry, error) {
	vars := map[string]Entry{}
	opts := &github.ListOptions{PerPage: 30} // API maximum for variables
	for {
		var res *github.ActionsVariables
		var resp *github.Response
		var err error
		switch {
		case s.IsOrg():
			res, resp, err = c.gh.Actions.ListOrgVariables(ctx, s.Org, opts)
		case s.IsEnv():
			res, resp, err = c.gh.Actions.ListEnvVariables(ctx, s.Org, s.Repo, s.Env, opts)
		default:
			res, resp, err = c.gh.Actions.ListRepoVariables(ctx, s.Org, s.Repo, opts)
		}
		if err != nil {
			return nil, wrap(err, s)
		}
		for _, v := range res.Variables {
			vars[v.Name] = Entry{Value: v.Value, UpdatedAt: v.GetUpdatedAt().Time}
		}
		if resp.NextPage == 0 {
			return vars, nil
		}
		opts.Page = resp.NextPage
	}
}

func (c *Client) PutVar(ctx context.Context, s scope.Scope, name, value string, exists bool, meta store.Meta) error {
	var err error
	switch {
	case s.IsOrg():
		vis := visibility(meta)
		var ids []int64
		if vis == "selected" {
			if ids, err = c.repoIDs(ctx, s.Org, meta.Repos); err != nil {
				return err
			}
		}
		if exists {
			_, err = c.gh.Actions.UpdateOrgVariable(ctx, s.Org, name, github.ActionsUpdateOrgVariableRequest{Value: &value, Visibility: &vis, SelectedRepositoryIDs: ids})
		} else {
			_, err = c.gh.Actions.CreateOrgVariable(ctx, s.Org, github.ActionsCreateOrgVariableRequest{Name: name, Value: value, Visibility: vis, SelectedRepositoryIDs: ids})
		}
	case s.IsEnv():
		if exists {
			_, err = c.gh.Actions.UpdateEnvVariable(ctx, s.Org, s.Repo, s.Env, name, github.ActionsUpdateVariableRequest{Value: &value})
		} else {
			_, err = c.gh.Actions.CreateEnvVariable(ctx, s.Org, s.Repo, s.Env, github.ActionsCreateVariableRequest{Name: name, Value: value})
		}
	default:
		if exists {
			_, err = c.gh.Actions.UpdateRepoVariable(ctx, s.Org, s.Repo, name, github.ActionsUpdateVariableRequest{Value: &value})
		} else {
			_, err = c.gh.Actions.CreateRepoVariable(ctx, s.Org, s.Repo, github.ActionsCreateVariableRequest{Name: name, Value: value})
		}
	}
	return wrap(err, s)
}

func (c *Client) DeleteVar(ctx context.Context, s scope.Scope, name string) error {
	var err error
	switch {
	case s.IsOrg():
		_, err = c.gh.Actions.DeleteOrgVariable(ctx, s.Org, name)
	case s.IsEnv():
		_, err = c.gh.Actions.DeleteEnvVariable(ctx, s.Org, s.Repo, s.Env, name)
	default:
		_, err = c.gh.Actions.DeleteRepoVariable(ctx, s.Org, s.Repo, name)
	}
	return wrap(err, s)
}
