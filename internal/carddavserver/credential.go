package carddavserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// CredentialFilename is the token-directory file holding the device
// credential. It stores a hash, never the password.
const CredentialFilename = "carddav-served.json" // #nosec G101 -- a filename, not a credential value.

const (
	maxCredentialFileBytes = 64 << 10
	// argon2id parameters follow the OWASP minimum for interactive logins.
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
	// GeneratedPasswordLength is the length of a generated device password.
	GeneratedPasswordLength = 32
)

// ErrNoCredential reports that no device credential has been set.
var ErrNoCredential = errors.New("no CardDAV device credential is set")

// Credential is the on-disk device credential.
type Credential struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
}

// Verify reports whether the presented username and password match.
func (c Credential) Verify(username, password string) bool {
	if subtle.ConstantTimeCompare([]byte(c.Username), []byte(username)) != 1 {
		// Still run the hash so a wrong username costs the same as a wrong
		// password.
		_ = verifyPHC(c.PasswordHash, password)
		return false
	}
	return verifyPHC(c.PasswordHash, password)
}

// GeneratePassword returns a random alphanumeric password.
func GeneratePassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	var builder strings.Builder
	for range GeneratedPasswordLength {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate device password: %w", err)
		}
		builder.WriteByte(alphabet[index.Int64()])
	}
	return builder.String(), nil
}

// SaveCredential hashes the password and atomically writes the credential
// file with private permissions.
func SaveCredential(tokenDir, username, password string) (Credential, error) {
	username = strings.TrimSpace(username)
	if username == "" || strings.ContainsAny(username, ":\r\n") {
		return Credential{}, errors.New("device username must be non-empty and must not contain a colon")
	}
	if len(password) < 12 {
		return Credential{}, errors.New("device password must be at least 12 characters")
	}
	hash, err := hashPHC(password)
	if err != nil {
		return Credential{}, err
	}
	credential := Credential{Username: username, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	encoded, err := json.Marshal(credential, json.Deterministic(true))
	if err != nil {
		return Credential{}, fmt.Errorf("encode device credential: %w", err)
	}
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return Credential{}, fmt.Errorf("create token directory: %w", err)
	}
	temporary, err := os.CreateTemp(tokenDir, ".carddav-served-*.json")
	if err != nil {
		return Credential{}, fmt.Errorf("create device credential file: %w", err)
	}
	temporaryName := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryName) }
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		cleanup()
		return Credential{}, fmt.Errorf("secure device credential file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		cleanup()
		return Credential{}, fmt.Errorf("write device credential file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return Credential{}, fmt.Errorf("sync device credential file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return Credential{}, fmt.Errorf("close device credential file: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(tokenDir, CredentialFilename)); err != nil {
		cleanup()
		return Credential{}, fmt.Errorf("publish device credential file: %w", err)
	}
	return credential, nil
}

// LoadCredential reads the credential file. A missing file is ErrNoCredential.
func LoadCredential(tokenDir string) (Credential, error) {
	file, err := os.Open(filepath.Join(tokenDir, CredentialFilename))
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, fmt.Errorf("open device credential file: %w", err)
	}
	defer file.Close() //nolint:errcheck // read-only file
	contents, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return Credential{}, fmt.Errorf("read device credential file: %w", err)
	}
	if len(contents) > maxCredentialFileBytes {
		return Credential{}, errors.New("device credential file is too large")
	}
	var credential Credential
	if err := json.Unmarshal(contents, &credential); err != nil {
		return Credential{}, fmt.Errorf("decode device credential file: %w", err)
	}
	if credential.Username == "" || !strings.HasPrefix(credential.PasswordHash, "$argon2id$") {
		return Credential{}, errors.New("device credential file is malformed")
	}
	return credential, nil
}

// ClearCredential removes the credential file. A missing file already is the
// desired state.
func ClearCredential(tokenDir string) error {
	err := os.Remove(filepath.Join(tokenDir, CredentialFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove device credential file: %w", err)
	}
	return nil
}

// hashPHC encodes an argon2id hash in PHC string format.
func hashPHC(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoding := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		encoding.EncodeToString(salt), encoding.EncodeToString(key)), nil
}

func verifyPHC(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	if memory == 0 || memory > 256*1024 || iterations == 0 || iterations > 16 || threads == 0 {
		return false
	}
	encoding := base64.RawStdEncoding
	salt, err := encoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := encoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 || len(expected) > 128 {
		return false
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(expected))) // #nosec G115 -- bounded above.
	return subtle.ConstantTimeCompare(key, expected) == 1
}
