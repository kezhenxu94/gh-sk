package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/kezhenxu94/gh-sk/internal/pusher"
	"github.com/kezhenxu94/gh-sk/internal/remote"
	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

type pushOpts struct {
	all, prune, force, dryRun, secretsOnly, varsOnly bool
}

func (o *pushOpts) kinds() []store.Kind {
	switch {
	case o.secretsOnly:
		return []store.Kind{store.Secret}
	case o.varsOnly:
		return []store.Kind{store.Variable}
	}
	return []store.Kind{store.Secret, store.Variable}
}

// targetScopes returns the scope argument, or every keychain scope with --all.
func targetScopes(args []string, all bool) ([]scope.Scope, error) {
	if all {
		if len(args) > 0 {
			return nil, errors.New("--all takes no scope argument")
		}
		items, err := listAll("")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var scopes []scope.Scope
		for _, it := range items {
			if seen[it.Scope] {
				continue
			}
			seen[it.Scope] = true
			sc, err := scope.Parse(it.Scope)
			if err != nil {
				return nil, fmt.Errorf("keychain item %s/%s: %w", it.Scope, it.Name, err)
			}
			scopes = append(scopes, sc)
		}
		return scopes, nil
	}
	sArg := ""
	if len(args) == 1 {
		sArg = args[0]
	}
	sc, err := resolveScope(sArg)
	return []scope.Scope{sc}, err
}

// localItems returns the keychain items of one exact scope; variable values are loaded.
func localItems(kind store.Kind, sc scope.Scope) ([]store.Item, error) {
	all, err := st.List(kind)
	if err != nil {
		return nil, err
	}
	var items []store.Item
	for _, it := range all {
		if it.Scope != sc.String() {
			continue
		}
		if kind == store.Variable {
			if it, err = st.Get(kind, it.Scope, it.Name); err != nil {
				return nil, err
			}
		}
		items = append(items, it)
	}
	return items, nil
}

func listRemote(ctx context.Context, rc remote.Remote, kind store.Kind, sc scope.Scope) (map[string]remote.Entry, error) {
	if kind == store.Variable {
		return rc.ListVars(ctx, sc)
	}
	return rc.ListSecrets(ctx, sc)
}

func fmtTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }

func describe(a pusher.Action, r map[string]remote.Entry) string {
	switch a.Op {
	case pusher.Drift:
		return fmt.Sprintf("changed on GitHub at %s, last synced version from %s; use --force to overwrite",
			fmtTime(r[a.Name].UpdatedAt), fmtTime(a.Local.Meta.GitHubUpdatedAt))
	case pusher.Untracked:
		return "on GitHub only; track it with `gh sk set` (or `gh sk pull -f FILE`), or remove with --prune"
	case pusher.Update:
		if a.Kind == store.Secret && a.Local.Meta.SyncedAt.IsZero() && a.Local.Meta.GitHubUpdatedAt.IsZero() {
			return "never synced by gh sk"
		}
	}
	return ""
}

// run plans (and unless dryRun, applies) every scope/kind; it returns an
// error when anything failed or drifted.
func run(o *pushOpts, args []string, apply bool) error {
	scopes, err := targetScopes(args, o.all)
	if err != nil {
		return err
	}
	rc, err := newRemote()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var errs []error
	drifts := 0
	for _, sc := range scopes {
		fmt.Fprintln(stdout, sc)
		for _, kind := range o.kinds() {
			local, err := localItems(kind, sc)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			rem, err := listRemote(ctx, rc, kind, sc)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			prune := o.prune
			if prune && len(local) == 0 && len(rem) > 0 {
				// Guard against wiping a scope that was never managed by gh sk.
				fmt.Fprintf(stderr, "  not pruning %ss: none stored in keychain for %s\n", kind.Short(), sc)
				prune = false
			}
			acts := pusher.Plan(sc, kind, local, rem, prune, o.force)
			for _, a := range acts {
				if a.Op == pusher.Drift {
					drifts++
				}
				fmt.Fprintf(stdout, "  %-9s  %-6s  %-30s  %s\n", a.Op, kind.Short(), a.Name, describe(a, rem))
			}
			if apply && !o.dryRun {
				if err := pusher.Apply(ctx, st, rc, sc, kind, acts); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if drifts > 0 {
		errs = append(errs, fmt.Errorf("%d item(s) changed on GitHub since the last sync were skipped; rerun with --force to overwrite", drifts))
	}
	return errors.Join(errs...)
}

func addScopeFlags(c *cobra.Command, o *pushOpts) {
	c.Flags().BoolVar(&o.all, "all", false, "every scope stored in the keychain")
	c.Flags().BoolVar(&o.secretsOnly, "secrets-only", false, "only secrets")
	c.Flags().BoolVar(&o.varsOnly, "vars-only", false, "only variables")
	c.MarkFlagsMutuallyExclusive("secrets-only", "vars-only")
}

func statusCmd() *cobra.Command {
	o := &pushOpts{}
	c := &cobra.Command{
		Use:   "status [scope]",
		Short: "Compare keychain with GitHub: create/update/ok/drift/untracked",
		Args:  cobra.MaximumNArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return run(o, args, false) },
	}
	addScopeFlags(c, o)
	return c
}

func pushCmd() *cobra.Command {
	o := &pushOpts{}
	c := &cobra.Command{
		Use:   "push [scope]",
		Short: "Push keychain secrets and variables to GitHub",
		Long: `Push keychain secrets and variables to GitHub.

Items unchanged since the last sync are skipped. Items changed on GitHub after
the last sync (drift) are skipped unless --force is given. Items that exist on
GitHub but not in the keychain are reported, and deleted with --prune.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return run(o, args, true) },
	}
	addScopeFlags(c, o)
	c.Flags().BoolVar(&o.prune, "prune", false, "delete items on GitHub that are not in the keychain")
	c.Flags().BoolVar(&o.force, "force", false, "overwrite items changed on GitHub since the last sync")
	c.Flags().BoolVar(&o.dryRun, "dry-run", false, "print the plan without changing anything")
	return c
}

func pullCmd() *cobra.Command {
	var force bool
	var secretsFile string
	c := &cobra.Command{
		Use:   "pull [scope]",
		Short: "Copy Actions variables from GitHub into the keychain (secrets cannot be read back)",
		Long: `Copy Actions variables from GitHub into the keychain.

Secret values cannot be read from GitHub. With --secrets-file, the names of
secrets on GitHub that are not in the keychain are written to a dotenv file
with empty values; fill them in and run ` + "`gh sk import -f FILE`" + ` to track them.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			scopes, err := targetScopes(args, false)
			if err != nil {
				return err
			}
			sc := scopes[0]
			rc, err := newRemote()
			if err != nil {
				return err
			}
			if secretsFile != "" {
				if err := writeSecretsTemplate(context.Background(), rc, sc, secretsFile); err != nil {
					return err
				}
			}
			rem, err := rc.ListVars(context.Background(), sc)
			if err != nil {
				return err
			}
			local, err := localItems(store.Variable, sc)
			if err != nil {
				return err
			}
			byName := map[string]store.Item{}
			for _, it := range local {
				byName[it.Name] = it
			}
			names := make([]string, 0, len(rem))
			for n := range rem {
				names = append(names, n)
			}
			sort.Strings(names)
			now := time.Now()
			skipped := 0
			for _, name := range names {
				r := rem[name]
				it, exists := byName[name]
				meta := it.Meta
				meta.SyncedAt, meta.GitHubUpdatedAt = now, r.UpdatedAt
				op := "ok"
				switch {
				case exists && it.Value == r.Value:
					err = st.SetMeta(store.Variable, sc.String(), name, meta)
				case exists && it.ChangedSinceSync() && !force:
					op, skipped = "skipped", skipped+1
				default:
					op = map[bool]string{true: "updated", false: "created"}[exists]
					err = st.Set(store.Item{Kind: store.Variable, Scope: sc.String(), Name: name, Value: r.Value, Meta: meta})
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "  %-8s  var  %s\n", op, name)
			}
			if skipped > 0 {
				return fmt.Errorf("%d variable(s) changed locally since the last sync were skipped; rerun with --force to overwrite", skipped)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite variables changed locally since the last sync")
	c.Flags().StringVarP(&secretsFile, "secrets-file", "f", "", "write untracked secret names to this new dotenv file")
	return c
}

// writeSecretsTemplate writes NAME= lines for secrets on GitHub that are not in
// the keychain. The file is created 0600 and never overwritten, as it will hold
// plaintext values once filled in.
func writeSecretsTemplate(ctx context.Context, rc remote.Remote, sc scope.Scope, path string) error {
	rem, err := rc.ListSecrets(ctx, sc)
	if err != nil {
		return err
	}
	local, err := localItems(store.Secret, sc)
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	for _, it := range local {
		tracked[it.Name] = true
	}
	var names []string
	for n := range rem {
		if !tracked[n] {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		fmt.Fprintf(stderr, "all secrets of %s are already in the keychain; %s not written\n", sc, path)
		return nil
	}
	sort.Strings(names)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "# Secrets of %s not in the keychain. Fill in the values, then run:\n", sc)
	fmt.Fprintf(f, "#   gh sk import %s -f %s && rm %s\n", sc, path, path)
	fmt.Fprintf(f, "# Lines left empty are skipped by import.\n")
	for _, n := range names {
		fmt.Fprintf(f, "%s=\n", n)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "wrote %d untracked secret name(s) to %s\n", len(names), path)
	return nil
}
