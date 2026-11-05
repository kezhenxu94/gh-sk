package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kezhenxu94/gh-sk/internal/scope"
	"github.com/kezhenxu94/gh-sk/internal/store"
)

// itemsWithValues returns the keychain items of one exact scope, values loaded, sorted by name.
func itemsWithValues(kind store.Kind, sc scope.Scope) ([]store.Item, error) {
	items, err := localItems(kind, sc)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if kind == store.Secret { // variables are already loaded by localItems
			if items[i], err = st.Get(kind, items[i].Scope, items[i].Name); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// dotenvLine renders NAME='value'. parseDotenv strips exactly one pair of
// matching outer quotes, so any single-line value reads back unchanged.
func dotenvLine(name, value string) string {
	return name + "='" + value + "'"
}

func exportCmd() *cobra.Command {
	var isVar bool
	var file string
	c := &cobra.Command{
		Use:   "export [scope] -f FILE",
		Short: "Write the scope's keychain values to a new dotenv file (plaintext; prefer `gh sk exec`)",
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
			kind := kindOf(isVar)
			items, err := itemsWithValues(kind, sc)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return fmt.Errorf("no %ss stored in keychain for %s", kind.Short(), sc)
			}
			// 0600 and never overwrite: the file holds plaintext values.
			f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			fmt.Fprintf(f, "# %ss of %s exported by gh sk. Plaintext: do not commit, delete after use.\n", kind.Short(), sc)
			n := 0
			for _, it := range items {
				if strings.ContainsAny(it.Value, "\r\n") {
					fmt.Fprintf(stderr, "skipped %s %s/%s: multi-line values cannot be written to a dotenv line\n", kind.Short(), sc, it.Name)
					continue
				}
				fmt.Fprintln(f, dotenvLine(it.Name, it.Value))
				n++
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "wrote %d %s(s) to %s\n", n, kind.Short(), file)
			return nil
		},
	}
	c.Flags().BoolVar(&isVar, "var", false, "export Actions variables instead of secrets")
	c.Flags().StringVarP(&file, "file", "f", "", "new dotenv file to write")
	_ = c.MarkFlagRequired("file")
	return c
}

// execEnv returns the current environment with the scope's secrets and
// variables added, replacing any variables of the same name.
func execEnv(sc scope.Scope) ([]string, error) {
	vals := map[string]string{}
	for _, kind := range []store.Kind{store.Secret, store.Variable} {
		items, err := itemsWithValues(kind, sc)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			vals[it.Name] = it.Value
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !hasKey(vals, k) {
			env = append(env, kv)
		}
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		env = append(env, k+"="+vals[k])
	}
	return env, nil
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

func execCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec [scope] -- COMMAND [ARGS...]",
		Short: "Run a command with the scope's secrets and variables as environment variables",
		Long: `Run a command with the scope's secrets and variables as environment variables.
Nothing is written to disk. Org-level items are not included for repo scopes.`,
		Example: "  gh sk exec -- npm run dev\n  gh sk exec my-org/my-repo/env/prod -- ./deploy.sh",
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash == len(args) {
				return errors.New("missing command: gh sk exec [scope] -- COMMAND [ARGS...]")
			}
			if dash > 1 {
				return errors.New("at most one scope may precede --")
			}
			sArg := ""
			if dash == 1 {
				sArg = args[0]
			}
			sc, err := resolveScope(sArg)
			if err != nil {
				return err
			}
			env, err := execEnv(sc)
			if err != nil {
				return err
			}
			path, err := exec.LookPath(args[dash])
			if err != nil {
				return err
			}
			// Replace this process so signals and the exit code pass straight through.
			return execve(path, args[dash:], env)
		},
	}
}
