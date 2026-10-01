//go:build sqlite_vec

package cmd

// precheckTestMainPath is a SQLite filesystem path for precheckVectorFeatures
// tests built with sqlite_vec, allowing the cron and validation paths to run
// without failing first on missing build-tag support.
const precheckTestMainPath = "/tmp/msgvault.db"
