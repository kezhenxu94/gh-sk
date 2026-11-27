package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kezhenxu94/gh-sk/internal/store"
)

// openStore opens a backend by name, replaced in tests.
var openStore = store.Open

func migrateCmd() *cobra.Command {
	var from, to string
	var all, force bool
	c := &cobra.Command{
		Use:   "migrate [scope] --from BACKEND --to BACKEND",
		Short: "Copy secrets and variables, with their metadata, from one backend to another",
		Long: `Copy the secrets and variables of a scope and its children (default: the
current repo; every scope with --all) from one backend to another: keychain
or gopass. Values (including multi-line ones), org visibility and sync state
are kept, so items unchanged since their last push stay "ok". Nothing is
written to disk and the source is left untouched.

Items already in the destination with a different value are skipped unless
--force.`,
		Example: "  gh sk migrate --all --from keychain --to gopass",
		Args:    cobra.MaximumNArgs(1),
		// migrate opens the two named backends, not the default one.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(_ *cobra.Command, args []string) error {
			if from == to {
				return errors.New("--from and --to must be different backends")
			}
			prefix, err := scopePrefix(args, all)
			if err != nil {
				return err
			}
			src, err := openStore(from)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			dst, err := openStore(to)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}
			items, err := listAll(src, prefix)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return fmt.Errorf("nothing stored in %s for %s", from, prefix)
			}
			skipped := 0
			var writes []store.Item
			var lines []string
			for _, it := range items {
				it, err := src.Get(it.Kind, it.Scope, it.Name)
				if err != nil {
					return err
				}
				op := "copied"
				old, err := dst.Get(it.Kind, it.Scope, it.Name)
				switch {
				case err == nil && old.Value == it.Value:
					op = "same"
				case err == nil && !force:
					op, skipped = "skipped", skipped+1
				case err == nil:
					op = "replaced"
				case !errors.Is(err, store.ErrNotFound):
					return err
				}
				if op == "copied" || op == "replaced" {
					if !it.ChangedSinceSync() {
						// The destination stamps its own modification time.
						it.Meta.SyncedAt = time.Now()
					}
					writes = append(writes, it)
				}
				lines = append(lines, fmt.Sprintf("  %-8s  %-6s  %s/%s\n", op, it.Kind.Short(), it.Scope, it.Name))
			}
			// One batch, so gopass commits and pushes once.
			if err := store.SetAll(dst, writes); err != nil {
				return err
			}
			for _, l := range lines {
				fmt.Fprint(stdout, l)
			}
			if skipped > 0 {
				return fmt.Errorf("%d item(s) already in %s with a different value were skipped; rerun with --force to overwrite", skipped, to)
			}
			return nil
		},
	}
	c.Flags().StringVar(&from, "from", "", "backend to copy from: keychain or gopass")
	c.Flags().StringVar(&to, "to", "", "backend to copy to: keychain or gopass")
	c.Flags().BoolVar(&all, "all", false, "every scope in the source backend")
	c.Flags().BoolVar(&force, "force", false, "overwrite items whose value differs in the destination")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}
