package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dunkbing/coldship/internal/apierr"
	"github.com/dunkbing/coldship/internal/client"
)

// cliConfig is what `coldship login` saves, so commands work without
// COLDSHIP_URL and COLDSHIP_TOKEN. Flags and env vars still win.
type cliConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "coldship", "config.json"), nil
}

func loadConfig() cliConfig {
	var c cliConfig
	p, err := configPath()
	if err != nil {
		return c
	}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveConfig(c cliConfig) (string, error) {
	p, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	// The token is a credential: only this user may read the file.
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return p, os.Chmod(p, 0o600)
}

const defaultURL = "http://127.0.0.1:7070"

// serverURL and token resolve flag > env > config file > default.
func serverURL() string {
	for _, v := range []string{flagURL, os.Getenv("COLDSHIP_URL"), loadConfig().URL} {
		if v != "" {
			return v
		}
	}
	return defaultURL
}

func token() string {
	for _, v := range []string{flagToken, os.Getenv("COLDSHIP_TOKEN"), loadConfig().Token} {
		if v != "" {
			return v
		}
	}
	return ""
}

func loginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login [server-url]",
		Short: "Save a server URL and token, so other commands don't need COLDSHIP_URL and COLDSHIP_TOKEN",
		Long: `Save a server URL and API token in your CLI config. The token is checked first.

  coldship login                                   # the server on this machine
  coldship login https://coldship.example.com      # asks for the token

Pass --token to skip the prompt. Flags and env vars still override the saved values.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u := defaultURL
			if len(args) == 1 {
				u = strings.TrimRight(args[0], "/")
			} else if flagURL != "" {
				u = flagURL
			}
			tok := flagToken
			if tok == "" {
				t, err := promptToken()
				if err != nil {
					return err
				}
				tok = t
			}
			me, err := client.New(u, tok).WhoAmI(cmd.Context())
			if err != nil {
				return err
			}
			p, err := saveConfig(cliConfig{URL: u, Token: tok})
			if err != nil {
				return err
			}
			return emit(map[string]any{"url": u, "name": me.Name, "scope": me.Scope, "config": p}, func() {
				fmt.Printf("Logged in to %s as %s (%s). Saved to %s.\n", u, me.Name, me.Scope, p)
			})
		},
	}
	return cmd
}

func promptToken() (string, error) {
	if !isTerminal(os.Stdin) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", apierr.New(400, "token_required", "no token given").WithHint("pass --token cst_...")
		}
		return strings.TrimSpace(line), nil
	}
	fmt.Fprint(os.Stderr, "API token: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if t := strings.TrimSpace(string(b)); t != "" {
		return t, nil
	}
	return "", apierr.New(400, "token_required", "no token given").WithHint("the admin token is printed when Coldship is installed")
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved server URL and token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := configPath()
			if err != nil {
				return err
			}
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return emit(map[string]any{"logged_out": true}, func() { fmt.Println("Logged out.") })
		},
	}
}
