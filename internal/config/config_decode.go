package config

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// configDecode preserves per-entry selector presence while parsing TOML once.
// Config stays the public, concrete runtime representation.
type configDecode struct {
	*Config

	Fastmail []fastmailDecode `toml:"fastmail"`
	IMAP     struct {
		Drafts []imapDraftDecode `toml:"drafts"`
	} `toml:"imap"`
	Gmail struct {
		Drafts []gmailDraftDecode `toml:"drafts"`
	} `toml:"gmail"`
}

type fastmailDecode struct {
	FastmailSource

	SourceID *int64 `toml:"source_id"`
}
type imapDraftDecode struct {
	IMAPDraftSource

	SourceID *int64 `toml:"source_id"`
}
type gmailDraftDecode struct {
	GmailDraftSource

	SourceID *int64 `toml:"source_id"`
}

func (w *configDecode) applySources() {
	for _, source := range w.Fastmail {
		if source.SourceID != nil {
			source.FastmailSource.SourceID = *source.SourceID
		}
		w.Config.Fastmail = append(w.Config.Fastmail, source.FastmailSource)
	}
	for _, source := range w.IMAP.Drafts {
		if source.SourceID != nil {
			source.IMAPDraftSource.SourceID = *source.SourceID
		}
		w.Config.IMAP.Drafts = append(w.Config.IMAP.Drafts, source.IMAPDraftSource)
	}
	for _, source := range w.Gmail.Drafts {
		if source.SourceID != nil {
			source.GmailDraftSource.SourceID = *source.SourceID
		}
		w.Config.Gmail.Drafts = append(w.Config.Gmail.Drafts, source.GmailDraftSource)
	}
}

func (w *configDecode) fastmailSourceIDs() []bool {
	configured := make([]bool, len(w.Fastmail))
	for i, source := range w.Fastmail {
		configured[i] = source.SourceID != nil
	}
	return configured
}

// validateDraftSelector keeps the identical selector contract for both providers.
func validateDraftSelector(section string, index int, id *int64, seen map[int64]struct{}) error {
	if id == nil {
		return fmt.Errorf("[[%s.drafts]] entry %d: source_id is required", section, index+1)
	}
	if *id <= 0 {
		return fmt.Errorf("[[%s.drafts]] entry %d: source_id must be positive", section, index+1)
	}
	if _, ok := seen[*id]; ok {
		return fmt.Errorf("[[%s.drafts]] entry %d: duplicate source_id selector %d", section, index+1, *id)
	}
	seen[*id] = struct{}{}
	return nil
}

func (c *Config) validateIMAPDraftSources(raw []imapDraftDecode) error {
	seen := make(map[int64]struct{}, len(raw))
	for i, source := range raw {
		if err := validateDraftSelector("imap", i, source.SourceID, seen); err != nil {
			return err
		}
		draft := c.IMAP.Drafts[i]
		if !utf8.ValidString(draft.Mailbox) || strings.TrimSpace(draft.Mailbox) == "" {
			return fmt.Errorf("[[imap.drafts]] entry %d: mailbox must be nonblank UTF-8", i+1)
		}
		if strings.ContainsAny(draft.Mailbox, "\x00\r\n") {
			return fmt.Errorf("[[imap.drafts]] entry %d: mailbox contains control characters", i+1)
		}
	}
	return nil
}

func (c *Config) validateGmailDraftSources(raw []gmailDraftDecode) error {
	seen := make(map[int64]struct{}, len(raw))
	for i, source := range raw {
		if err := validateDraftSelector("gmail", i, source.SourceID, seen); err != nil {
			return err
		}
	}
	return nil
}

// normalizePaths expands home-relative paths, then resolves relative paths
// when an explicit config location supplies the base directory.
func (c *Config) normalizePaths(explicit bool) {
	normalize := func(path string) string {
		path = expandPath(path)
		if explicit {
			path = resolveRelative(path, c.HomeDir)
		}
		return path
	}
	for _, path := range []*string{
		&c.Data.DataDir, &c.Data.ExportDir, &c.Log.Dir,
		&c.OAuth.ClientSecrets, &c.OAuth.ServiceAccountKey,
		&c.Vector.DBPath, &c.Vector.Multimodal.CapabilitiesFile, &c.Backup.Repo,
	} {
		*path = normalize(*path)
	}
	for name, app := range c.OAuth.Apps {
		app.ClientSecrets = normalize(app.ClientSecrets)
		app.ServiceAccountKey = normalize(app.ServiceAccountKey)
		c.OAuth.Apps[name] = app
	}
}
