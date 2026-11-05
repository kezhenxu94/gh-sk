package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kezhenxu94/gh-sk/internal/remote"
	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

func init() {
	root.AddCommand(setCmd(), getCmd(), lsCmd(), rmCmd(), importCmd())
}

func orgMeta(sc scope.Scope, visibility string, repos []string) (store.Meta, error) {
	if visibility == "" && len(repos) == 0 {
		return store.Meta{}, nil
	}
	if !sc.IsOrg() {
		return store.Meta{}, errors.New("--visibility/--repos only apply to org scopes")
	}
	switch visibility {
	case "all", "private":
		if len(repos) > 0 {
			return store.Meta{}, errors.New("--repos requires --visibility selected")
		}
	case "selected":
		if len(repos) == 0 {
			return store.Meta{}, errors.New("--visibility selected requires --repos")
		}
	default:
		return store.Meta{}, fmt.Errorf("invalid --visibility %q: want all, private or selected", visibility)
	}
	return store.Meta{Visibility: visibility, Repos: repos}, nil
}

func setCmd() *cobra.Command {
	var isVar bool
	var visibility, body string
	var repos []string
	c := &cobra.Command{
		Use:   "set [scope] NAME",
		Short: "Store a secret (or variable) in the keychain; value from --body, a hidden prompt or stdin",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sc, name, err := scopeAndName(args)
			if err != nil {
				return err
			}
			meta, err := orgMeta(sc, visibility, repos)
			if err != nil {
				return err
			}
			value := body
			if !cmd.Flags().Changed("body") {
				if value, err = readValue(name); err != nil {
					return err
				}
			}
			kind := kindOf(isVar)
			if err := st.Set(store.Item{Kind: kind, Scope: sc.String(), Name: name, Meta: meta, Value: value}); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "saved %s %s/%s (run `gh sk push` to push it to GitHub)\n", kind.Short(), sc, name)
			return nil
		},
	}
	c.Flags().BoolVar(&isVar, "var", false, "store an Actions variable instead of a secret")
	c.Flags().StringVarP(&body, "body", "b", "", "the value (visible in shell history; omit to be prompted or pipe via stdin)")
	c.Flags().StringVar(&visibility, "visibility", "", "org scopes: all, private (default on GitHub) or selected")
	c.Flags().StringSliceVar(&repos, "repos", nil, "org scopes with --visibility selected: repository names")
	return c
}

func getCmd() *cobra.Command {
	var isVar bool
	c := &cobra.Command{
		Use:   "get [scope] NAME",
		Short: "Print a value from the keychain",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			sc, name, err := scopeAndName(args)
			if err != nil {
				return err
			}
			it, err := st.Get(kindOf(isVar), sc.String(), name)
			if err != nil {
				return err
			}
			fmt.Println(it.Value)
			return nil
		},
	}
	c.Flags().BoolVar(&isVar, "var", false, "get an Actions variable instead of a secret")
	return c
}

// listAll returns keychain items of both kinds, optionally limited to a scope and its children.
func listAll(prefix string) ([]store.Item, error) {
	var all []store.Item
	for _, k := range []store.Kind{store.Secret, store.Variable} {
		items, err := st.List(k)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if prefix == "" || it.Scope == prefix || strings.HasPrefix(it.Scope, prefix+"/") {
				all = append(all, it)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Scope != all[j].Scope {
			return all[i].Scope < all[j].Scope
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls [scope]",
		Short: "List stored names by scope (never values); without scope lists everything",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			prefix := ""
			if len(args) == 1 {
				sc, err := scope.Parse(args[0])
				if err != nil {
					return err
				}
				prefix = sc.String()
			}
			items, err := listAll(prefix)
			if err != nil {
				return err
			}
			last := ""
			for _, it := range items {
				if it.Scope != last {
					fmt.Println(it.Scope)
					last = it.Scope
				}
				synced := "never synced"
				if !it.Meta.SyncedAt.IsZero() {
					synced = "synced " + it.Meta.SyncedAt.Local().Format("2006-01-02 15:04")
					if it.ChangedSinceSync() {
						synced += ", changed since"
					}
				} else if !it.Meta.GitHubUpdatedAt.IsZero() {
					synced = "changed since last sync"
				}
				extra := ""
				if it.Meta.Visibility != "" {
					extra = " visibility=" + it.Meta.Visibility
					if len(it.Meta.Repos) > 0 {
						extra += " repos=" + strings.Join(it.Meta.Repos, ",")
					}
				}
				fmt.Printf("  %-6s  %-30s  %s%s\n", it.Kind.Short(), it.Name, synced, extra)
			}
			return nil
		},
	}
}

func rmCmd() *cobra.Command {
	var isVar, alsoRemote bool
	c := &cobra.Command{
		Use:   "rm [scope] NAME",
		Short: "Delete from the keychain (and from GitHub with --remote)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			sc, name, err := scopeAndName(args)
			if err != nil {
				return err
			}
			kind := kindOf(isVar)
			if alsoRemote {
				rc, err := remote.New()
				if err != nil {
					return err
				}
				del := rc.DeleteSecret
				if isVar {
					del = rc.DeleteVar
				}
				if err := del(context.Background(), sc, name); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "deleted %s %s/%s on GitHub\n", kind.Short(), sc, name)
			}
			if err := st.Delete(kind, sc.String(), name); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "deleted %s %s/%s from keychain\n", kind.Short(), sc, name)
			return nil
		},
	}
	c.Flags().BoolVar(&isVar, "var", false, "delete an Actions variable instead of a secret")
	c.Flags().BoolVar(&alsoRemote, "remote", false, "also delete it on GitHub")
	return c
}

func importCmd() *cobra.Command {
	var isVar bool
	var file string
	c := &cobra.Command{
		Use:   "import [scope] -f FILE",
		Short: "Store every KEY=VALUE of a dotenv file in the keychain",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sArg := ""
			if len(args) == 1 {
				sArg = args[0]
			}
			sc, err := resolveScope(sArg)
			if err != nil {
				return err
			}
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			vals, err := parseDotenv(f)
			if err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			normalized := map[string]string{}
			for k, v := range vals {
				name, err := store.NormalizeName(k)
				if err != nil {
					return fmt.Errorf("%s: %w", file, err)
				}
				normalized[name] = v
			}
			kind := kindOf(isVar)
			for name, v := range normalized {
				if v == "" {
					// Never store blanks: syncing them would wipe the value on GitHub.
					fmt.Fprintf(os.Stderr, "skipped %s %s/%s: empty value\n", kind.Short(), sc, name)
					continue
				}
				if err := st.Set(store.Item{Kind: kind, Scope: sc.String(), Name: name, Value: v}); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "saved %s %s/%s\n", kind.Short(), sc, name)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&isVar, "var", false, "import as Actions variables instead of secrets")
	c.Flags().StringVarP(&file, "file", "f", "", "dotenv file to import")
	_ = c.MarkFlagRequired("file")
	return c
}
