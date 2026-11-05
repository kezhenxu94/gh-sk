# ghsk

GitHub Actions secrets are write-only. `ghsk` keeps them (and Actions variables)
in the macOS Keychain as the source of truth and syncs them to GitHub.

## Install

```sh
go install github.com/kezhenxu94/github-secrets-keychain/cmd/ghsk@latest   # needs cgo (default on macOS)
```

Auth: `GITHUB_TOKEN` / `GH_TOKEN`, or `gh auth token`. Needs `repo` scope, plus
`admin:org` for org scopes.

## Scopes

| Scope | Keychain "Where" |
|---|---|
| org | `github.com/my-org` |
| repo | `github.com/my-org/my-repo` |
| environment | `github.com/my-org/my-repo/env/production` |

`github.com/` is optional on the command line; when the scope is omitted it comes
from the `origin` remote of the current git repo.

Each item is a generic password: service = scope, account = name, kind =
`github-secret` / `github-variable`, comment = JSON metadata (org visibility,
sync timestamps). A secret and a variable cannot share a name in one scope.

## Usage

```sh
ghsk set my-org/my-repo NPM_TOKEN                # prompts (hidden); or pipe the value in
ghsk set my-org ORG_TOKEN --visibility selected --repos api,web
ghsk set my-org/my-repo/env/prod REGION --var
ghsk import my-org/my-repo -f .env               # bulk load
ghsk ls                                          # names + sync state, never values
ghsk get my-org/my-repo NPM_TOKEN

ghsk status my-org/my-repo                       # create / update / ok / drift / untracked
ghsk sync my-org/my-repo --dry-run
ghsk sync --all --prune                          # every scope in the keychain
ghsk pull my-org/my-repo                         # variables only: GitHub -> keychain
ghsk rm my-org/my-repo OLD_TOKEN --remote
```

## Sync and drift

After each push ghsk records on the keychain item when it synced and GitHub's
`updated_at` for that item. On the next `status`/`sync`:

- **update**: changed in the keychain since the last sync (or never synced by ghsk).
- **ok**: unchanged on both sides; skipped.
- **drift**: GitHub's `updated_at` is newer than the recorded one, so someone
  changed it outside ghsk. Skipped (non-zero exit) unless `--force`.
- **untracked**: on GitHub only; deleted with `--prune`. Pruning is refused for
  a scope/kind with nothing in the keychain, to avoid wiping unmanaged repos.

Changes made with `ghsk set`/`import` are always detected. Edits made directly
in Keychain Access are detected through the item's modification date.

## Notes

- macOS asks for keychain access the first time the binary reads a value
  (choose "Always Allow"). Rebuilding changes the binary, so it asks again.
  Editing an item in Keychain Access or with `security add-generic-password -U`
  replaces its access list, so the next `ghsk sync` will prompt (and block) for it.
- A piped value has one trailing newline removed.
