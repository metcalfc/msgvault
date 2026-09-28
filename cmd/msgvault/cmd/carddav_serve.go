package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/msgvault/internal/carddavserver"
	"go.kenn.io/msgvault/internal/config"
)

// The served address book's credential lives in the daemon's token directory
// and is managed on the daemon host. These commands operate on the local
// configuration and token directory; they do not go through the API.

func newCardDAVServeCmd() *cobra.Command {
	root := &cobra.Command{Use: "serve", Short: "Manage the address book the daemon serves to devices"}
	root.AddCommand(&cobra.Command{
		Use: "status", Short: "Show served address book settings and credential state", Args: cobra.NoArgs,
		RunE: runCardDAVServeStatus,
	})
	password := &cobra.Command{Use: "password", Short: "Manage the device credential"}
	var username string
	set := &cobra.Command{
		Use: "set", Short: "Generate a device password and print it once", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runCardDAVServePasswordSet(cmd, username) },
	}
	set.Flags().StringVar(&username, "username", "msgvault", "username the device signs in with")
	password.AddCommand(set, &cobra.Command{
		Use: "clear", Short: "Remove the device credential", Args: cobra.NoArgs,
		RunE: runCardDAVServePasswordClear,
	})
	root.AddCommand(password)
	return root
}

func cardDAVServeConfig(cmd *cobra.Command) (*config.Config, error) {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return nil, errors.New("configuration is unavailable")
	}
	return state.cfg, nil
}

type cardDAVServeStatus struct {
	Enabled               bool       `json:"enabled"`
	DisplayName           string     `json:"display_name"`
	Path                  string     `json:"path"`
	CredentialUsername    string     `json:"credential_username,omitempty"`
	CredentialCreatedAt   *time.Time `json:"credential_created_at,omitempty"`
	AllowPlainHTTPFrom    []string   `json:"allow_plain_http_from"`
	RequireTailscaleLogin string     `json:"require_tailscale_login,omitempty"`
	Hint                  string     `json:"hint,omitempty"`
}

func runCardDAVServeStatus(cmd *cobra.Command, _ []string) error {
	cfg, err := cardDAVServeConfig(cmd)
	if err != nil {
		return err
	}
	displayName := cfg.CardDAV.Serve.DisplayName
	if displayName == "" {
		displayName = "msgvault"
	}
	status := cardDAVServeStatus{
		Enabled: cfg.CardDAV.Serve.Enabled, DisplayName: displayName,
		Path:                  carddavserver.DefaultPrefix + "/",
		AllowPlainHTTPFrom:    append([]string{}, cfg.CardDAV.Serve.AllowPlainHTTPFrom...),
		RequireTailscaleLogin: cfg.CardDAV.Serve.RequireTailscaleLogin,
	}
	credential, err := carddavserver.LoadCredential(cfg.TokensDir())
	switch {
	case err == nil:
		status.CredentialUsername = credential.Username
		created := credential.CreatedAt
		status.CredentialCreatedAt = &created
	case errors.Is(err, carddavserver.ErrNoCredential):
		status.Hint = "no device credential; run: msgvault carddav serve password set"
	default:
		return err
	}
	if !status.Enabled {
		status.Hint = "set [carddav.serve] enabled = true in config.toml and restart the daemon"
	}
	return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), status, json.Deterministic(true))
}

func runCardDAVServePasswordSet(cmd *cobra.Command, username string) error {
	cfg, err := cardDAVServeConfig(cmd)
	if err != nil {
		return err
	}
	password, err := carddavserver.GeneratePassword()
	if err != nil {
		return err
	}
	credential, err := carddavserver.SaveCredential(cfg.TokensDir(), username, password)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Device credential saved to %s\n\n", cfg.TokensDir())
	fmt.Fprintf(out, "  Username: %s\n", credential.Username)
	fmt.Fprintf(out, "  Password: %s\n\n", password)
	fmt.Fprintln(out, "The password is not stored and cannot be shown again. A running daemon uses it on the next request.")
	if !cfg.CardDAV.Serve.Enabled {
		fmt.Fprintln(out, "The served address book is disabled; set [carddav.serve] enabled = true and restart the daemon.")
	}
	return nil
}

func runCardDAVServePasswordClear(cmd *cobra.Command, _ []string) error {
	cfg, err := cardDAVServeConfig(cmd)
	if err != nil {
		return err
	}
	if err := carddavserver.ClearCredential(cfg.TokensDir()); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Device credential removed; devices can no longer sign in.")
	return nil
}
