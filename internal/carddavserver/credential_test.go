package carddavserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveLoadVerifyAndClearCredential(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := filepath.Join(t.TempDir(), "tokens")

	_, err := LoadCredential(dir)
	require.ErrorIs(err, ErrNoCredential)

	saved, err := SaveCredential(dir, "device", "correct-horse-battery-staple")
	require.NoError(err)
	assert.Equal("device", saved.Username)
	assert.Greater(len(saved.PasswordHash), 40)
	assert.NotContains(saved.PasswordHash, "correct-horse")

	info, err := os.Stat(filepath.Join(dir, CredentialFilename))
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())
	contents, err := os.ReadFile(filepath.Join(dir, CredentialFilename))
	require.NoError(err)
	assert.NotContains(string(contents), "correct-horse")

	loaded, err := LoadCredential(dir)
	require.NoError(err)
	assert.True(loaded.Verify("device", "correct-horse-battery-staple"))
	assert.False(loaded.Verify("device", "correct-horse-battery-stapl"))
	assert.False(loaded.Verify("Device", "correct-horse-battery-staple"))
	assert.False(loaded.Verify("", ""))

	require.NoError(ClearCredential(dir))
	_, err = LoadCredential(dir)
	require.ErrorIs(err, ErrNoCredential)
	require.NoError(ClearCredential(dir))
}

func TestSaveCredentialRejectsWeakInput(t *testing.T) {
	dir := t.TempDir()
	_, err := SaveCredential(dir, "", "correct-horse-battery-staple")
	require.Error(t, err)
	_, err = SaveCredential(dir, "a:b", "correct-horse-battery-staple")
	require.Error(t, err)
	_, err = SaveCredential(dir, "device", "short")
	require.Error(t, err)
}

func TestGeneratePasswordIsLongAndVaries(t *testing.T) {
	first, err := GeneratePassword()
	require.NoError(t, err)
	second, err := GeneratePassword()
	require.NoError(t, err)
	assert.Len(t, first, GeneratedPasswordLength)
	assert.NotEqual(t, first, second)
}

func TestVerifyPHCRejectsMalformedOrOversizedParameters(t *testing.T) {
	assert.False(t, verifyPHC("$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA", "x"))
	assert.False(t, verifyPHC("$argon2id$v=19$m=999999999,t=2,p=1$c2FsdA$aGFzaA", "x"))
	assert.False(t, verifyPHC("$argon2id$v=19$m=19456,t=2,p=1$not-base64!$aGFzaA", "x"))
	assert.False(t, verifyPHC("garbage", "x"))
}
