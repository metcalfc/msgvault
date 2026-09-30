package store

import "strings"

// Label roles beyond sent. Providers that mark folders canonically (Gmail
// system label IDs, IMAP RFC 6154 special-use attributes, Microsoft Graph
// well-known folders) record the role on the label, so a localized or
// renamed folder such as "Junk Email" or "Deleted Items" is still known.
const (
	// LabelSystemRoleJunk identifies a spam or junk folder.
	LabelSystemRoleJunk = "junk"
	// LabelSystemRoleTrash identifies a trash or deleted-items folder.
	LabelSystemRoleTrash = "trash"
)

// Folder names that mean junk or trash for sources that carry no role
// (mbox, PST, and older archives). Compared case-insensitively after
// trimming.
var (
	junkLabelNames = []string{
		"spam", "junk", "junk email", "junk e-mail", "bulk mail", "[gmail]/spam",
	}
	trashLabelNames = []string{
		"trash", "deleted items", "deleted messages", "bin", "[gmail]/trash", "[gmail]/bin",
	}
)

func sqlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return strings.Join(quoted, ", ")
}

// labelMatchSQL matches a label by its recorded role, its Gmail system ID,
// or, for sources without either, its name.
func labelMatchSQL(alias string, roles, sourceIDs, names []string) string {
	parts := []string{}
	if len(roles) > 0 {
		parts = append(parts, alias+".system_role IN ("+sqlStringList(roles)+")")
	}
	if len(sourceIDs) > 0 {
		parts = append(parts, alias+".source_label_id IN ("+sqlStringList(sourceIDs)+")")
	}
	if len(names) > 0 {
		parts = append(parts, "LOWER(TRIM("+alias+".name)) IN ("+sqlStringList(names)+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// junkLabelSQL matches spam and junk folders.
func junkLabelSQL(alias string) string {
	return labelMatchSQL(alias, []string{LabelSystemRoleJunk}, []string{"SPAM"}, junkLabelNames)
}

// junkOrTrashLabelSQL matches spam, junk, trash, and deleted-items folders.
func junkOrTrashLabelSQL(alias string) string {
	return labelMatchSQL(alias,
		[]string{LabelSystemRoleJunk, LabelSystemRoleTrash}, []string{"SPAM", "TRASH"},
		append(append([]string{}, junkLabelNames...), trashLabelNames...))
}

// starredLabelSQL matches the starred label.
func starredLabelSQL(alias string) string {
	return labelMatchSQL(alias, nil, []string{"STARRED"}, []string{"starred"})
}
