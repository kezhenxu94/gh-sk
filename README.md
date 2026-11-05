# gh sk

English | [简体中文](README.zh-CN.md)

A [gh](https://cli.github.com) extension. GitHub Actions secrets are write-only; `gh sk` keeps them (and Actions variables)
in the macOS Keychain as the source of truth and pushes them to GitHub.

## Install

```sh
gh extension install kezhenxu94/gh-sk   # macOS only
```

From source: `go build -o gh-sk . && gh extension install .` (the directory must be named `gh-sk`).

Auth: whatever `gh` is logged in with (or `GH_TOKEN`). Needs `repo` scope, plus
`admin:org` for org scopes.

## Scopes

| Scope | Keychain "Where" |
|---|---|
| org | `github.com/my-org` |
| repo | `github.com/my-org/my-repo` |
| environment | `github.com/my-org/my-repo/env/production` |

`github.com/` is optional on the command line. When the scope is omitted it is the
current repository, resolved like `gh` does: `GH_REPO`, `gh repo set-default`, then
the git remotes.

Each item is a generic password: service = scope, account = name, kind =
`github-secret` / `github-variable`, comment = JSON metadata (org visibility,
sync timestamps). A secret and a variable cannot share a name in one scope.

## Usage

```sh
gh sk set my-org/my-repo NPM_TOKEN                # prompts (hidden); or pipe the value in
gh sk set DATABASE_URL -b 'postgres://…'           # current repo; -b ends up in shell history
gh sk set my-org ORG_TOKEN --visibility selected --repos api,web
gh sk set my-org/my-repo/env/prod REGION --var
gh sk import my-org/my-repo -f .env               # bulk load
gh sk ls                                          # current repo: names + sync state, never values
gh sk ls --all                                    # every scope in the keychain
gh sk get my-org/my-repo NPM_TOKEN

gh sk status my-org/my-repo                       # create / update / ok / drift / untracked
gh sk push my-org/my-repo --dry-run
gh sk push --all --prune                          # every scope in the keychain
gh sk pull my-org/my-repo                         # variables: GitHub -> keychain
gh sk pull -f secrets.env                         # untracked secret names -> NAME= template;
                                                  # fill in, then: gh sk import -f secrets.env && rm secrets.env
gh sk rm my-org/my-repo OLD_TOKEN --remote
```

## Using secrets locally

```sh
gh sk exec -- npm run dev                         # secrets + variables of the current repo as env vars
gh sk exec my-org/my-repo/env/prod -- ./deploy.sh
gh sk export -f .env                              # NAME='value' dotenv file; --var for variables
```

Prefer `exec`: nothing touches the disk, multi-line values work, and the command's
exit code is passed through. Keychain values replace environment variables of the
same name; org-level items are not included for repo scopes.

`export` writes plaintext, so the file is created `0600`, never overwritten, and
should not be committed. Values are single-quoted so `gh sk import` reads them back
unchanged; multi-line values (e.g. PEM keys) cannot fit a dotenv line and are skipped
with a warning.

## Push and drift

After each push gh sk records on the keychain item when it synced and GitHub's
`updated_at` for that item. On the next `status`/`push`:

- **update**: changed in the keychain since the last sync (or never synced by gh sk).
- **ok**: unchanged on both sides; skipped.
- **drift**: GitHub's `updated_at` is newer than the recorded one, so someone
  changed it outside gh sk. Skipped (non-zero exit) unless `--force`.
- **untracked**: on GitHub only; deleted with `--prune`. Pruning is refused for
  a scope/kind with nothing in the keychain, to avoid wiping unmanaged repos.

Changes made with `gh sk set`/`import` are always detected. Edits made directly
in Keychain Access are detected through the item's modification date.

## Notes

- macOS asks for keychain access the first time gh sk reads a value (choose
  "Always Allow"). Release binaries are signed with a Developer ID under a fixed
  identifier, so that answer survives upgrades. Binaries you build yourself are
  ad-hoc signed, so macOS asks again after every rebuild.
  Editing an item in Keychain Access or with `security add-generic-password -U`
  replaces its access list, so the next `gh sk push` will prompt (and block) for it.
- A piped value has one trailing newline removed.
- Empty values never reach GitHub: `set` rejects them, `import` skips them and
  `push` refuses to send them.
- `import` skips `#` comment lines and strips one pair of matching outer quotes;
  inline `# comments` and escapes such as `\n` are kept literally.

## Development

```sh
go test ./...                              # unit + CLI tests with in-memory keychain and fake GitHub
GHSK_KEYCHAIN_TESTS=1 go test ./...        # also run against the real Keychain (writes, then deletes, test items)
```

CI runs everything, including the Keychain tests in a throwaway keychain, on a macOS
runner for every pull request and push to `main`; releases only publish after it passes.

Release signing uses the repository variable `MACOS_SIGNING_IDENTITY` and the secrets
`MACOS_CERTIFICATE_P12` (base64 `.p12`) and optional `MACOS_CERTIFICATE_PASSWORD`.
Without the variable, binaries are built unsigned.
