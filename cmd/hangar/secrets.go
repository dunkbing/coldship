package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spf13/cobra"

	"hangar/internal/apierr"
	"hangar/internal/secrets"
	"hangar/internal/store"
)

// defaultKeyFile is hangar.key next to the state database.
func defaultKeyFile(data string) string {
	abs, err := filepath.Abs(data)
	if err != nil {
		abs = data
	}
	return filepath.Join(filepath.Dir(abs), "hangar.key")
}

// openStore loads the secret key and opens the state database with it. It
// never creates a new key for a database whose secrets are already
// encrypted: that would make them unreadable.
func openStore(data, keyFile string, log *slog.Logger) (*store.Store, *secrets.Keyring, error) {
	if env := os.Getenv("HANGAR_SECRET_KEY"); env != "" {
		kr, err := secrets.FromEnv(env)
		if err != nil {
			return nil, nil, err
		}
		st, err := store.Open(data, kr)
		if errors.Is(err, store.ErrKeyMismatch) {
			return nil, nil, fmt.Errorf("HANGAR_SECRET_KEY cannot read the secrets in %s: %w", data, err)
		}
		return st, kr, err
	}
	if secrets.Exists(keyFile) {
		kr, err := secrets.Load(keyFile)
		if err != nil {
			return nil, nil, err
		}
		st, err := store.Open(data, kr)
		if errors.Is(err, store.ErrKeyMismatch) {
			return nil, nil, fmt.Errorf("the key in %s cannot read the secrets in %s; use the key file this database was encrypted with: %w", keyFile, data, err)
		}
		return st, kr, err
	}
	// No key yet. Save it before opening, so the database is never encrypted
	// with a key that isn't on disk; remove it again if it doesn't fit.
	kr := secrets.New()
	kr.Source = "file " + keyFile
	if err := kr.Save(keyFile); err != nil {
		return nil, nil, fmt.Errorf("creating the secret key file: %w", err)
	}
	st, err := store.Open(data, kr)
	if err != nil {
		os.Remove(keyFile)
		if errors.Is(err, store.ErrKeyMismatch) {
			return nil, nil, fmt.Errorf("the secrets in %s are encrypted, but the key file %s is missing. Restore it from your backup, "+
				"or set HANGAR_SECRET_KEY. Hangar did not create a new key, because it could not read the existing secrets", data, keyFile)
		}
		return nil, nil, err
	}
	log.Warn("created a new secret key: back it up separately from the database; without it, stored secrets cannot be recovered",
		"key_file", keyFile)
	return st, kr, nil
}

// keyRotator rotates a file-based key: the file first holds both keys, all
// secrets are re-encrypted, then the file keeps only the new key. A crash at
// any point leaves a key file that can read the database.
type keyRotator struct {
	mu      sync.Mutex
	st      *store.Store
	kr      *secrets.Keyring
	keyFile string
	fromEnv bool
}

func (r *keyRotator) rotate(ctx context.Context) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fromEnv {
		return nil, apierr.New(409, "key_from_environment", "the secret key comes from HANGAR_SECRET_KEY, which Hangar can't rewrite").
			WithHint("to rotate it: stop the server, run `hangar secrets decrypt`, set a new HANGAR_SECRET_KEY, and start the server again")
	}
	old := r.kr.CurrentID()
	next := r.kr.WithNewKey()
	if err := next.Save(r.keyFile); err != nil {
		return nil, err
	}
	n, err := r.st.RotateKey(ctx, next)
	if err != nil {
		return nil, fmt.Errorf("re-encrypting failed; the key file still holds both keys, so nothing is lost: %w", err)
	}
	final := next.OnlyCurrent()
	if err := final.Save(r.keyFile); err != nil {
		return nil, fmt.Errorf("secrets were re-encrypted, but dropping the old key from %s failed (it still works): %w", r.keyFile, err)
	}
	r.st.SetKeyring(final)
	r.kr = final
	return map[string]any{"previous_key": old, "key_id": final.CurrentID(), "values": n}, nil
}

func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Encryption of stored secrets"}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show how stored secrets are encrypted (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := newClient().SecretsStatus(cmd.Context())
			if err != nil {
				return err
			}
			return emit(st, func() {
				fmt.Printf("Secrets are encrypted with AES-256-GCM, key %s (%s).\n", st.KeyID, st.Source)
				keys := make([]string, 0, len(st.Encrypted))
				for k := range st.Encrypted {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("  %-20s %d encrypted\n", k, st.Encrypted[k])
				}
				if st.Plaintext > 0 {
					fmt.Printf("  %d values are still plaintext; they are encrypted when the server restarts.\n", st.Plaintext)
				}
			})
		},
	}

	rotate := &cobra.Command{
		Use:   "rotate-key",
		Short: "Re-encrypt all stored secrets with a new key (admin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := newClient().RotateSecretKey(cmd.Context())
			if err != nil {
				return err
			}
			return emit(res, func() {
				fmt.Printf("Rotated the secret key: %v → %v, %v values re-encrypted. Back up the key file again.\n",
					res["previous_key"], res["key_id"], res["values"])
			})
		},
	}

	var data, keyFile string
	var yes bool
	decrypt := &cobra.Command{
		Use:   "decrypt",
		Short: "Write all stored secrets back as plaintext (offline, for downgrades)",
		Long: "Decrypt every stored secret in place, so an older Hangar version that predates encryption can\n" +
			"read the database. Stop the server first. Afterwards the database holds secrets in plaintext;\n" +
			"a newer Hangar encrypts them again when it starts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return fmt.Errorf("this writes every secret in %s as plaintext; stop the server, then run again with --yes", data)
			}
			if keyFile == "" {
				keyFile = defaultKeyFile(data)
			}
			var kr *secrets.Keyring
			var err error
			if env := os.Getenv("HANGAR_SECRET_KEY"); env != "" {
				kr, err = secrets.FromEnv(env)
			} else {
				kr, err = secrets.Load(keyFile)
			}
			if err != nil {
				return err
			}
			n, err := store.DecryptAll(cmd.Context(), data, kr)
			if err != nil {
				return err
			}
			fmt.Printf("Decrypted %d values in %s. The database now holds secrets in plaintext.\n", n, data)
			return nil
		},
	}
	decrypt.Flags().StringVar(&data, "data", envOr("HANGAR_DATA", "hangar.db"), "state database")
	decrypt.Flags().StringVar(&keyFile, "key-file", envOr("HANGAR_SECRET_KEY_FILE", ""), "secret key file (default: hangar.key next to --data)")
	decrypt.Flags().BoolVar(&yes, "yes", false, "confirm")

	cmd.AddCommand(status, rotate, decrypt)
	return cmd
}
