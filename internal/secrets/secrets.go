// Package secrets encrypts values at rest with AES-256-GCM.
//
// A sealed value looks like "hgs1:<key id>:<base64 nonce+ciphertext>". Each
// value is bound to where it is stored (its "additional data", e.g.
// "webhooks.secret:shop"), so a ciphertext copied into another row does not
// decrypt. Keys live in a keyring so they can be rotated: values sealed with
// any key in the ring open, and new values use the current key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const prefix = "hgs1:"

// Keyring holds the encryption keys. It is safe for concurrent use.
type Keyring struct {
	mu      sync.RWMutex
	current string
	keys    map[string][]byte
	// Source describes where the keys came from, for status output.
	Source string
}

type file struct {
	Version int               `json:"version"`
	Current string            `json:"current"`
	Keys    map[string]string `json:"keys"`
}

// New returns a keyring with one fresh random key.
func New() *Keyring {
	k := &Keyring{keys: map[string][]byte{}}
	k.addKey()
	return k
}

func (k *Keyring) addKey() string {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	idb := make([]byte, 4)
	rand.Read(idb)
	id := hex.EncodeToString(idb)
	k.keys[id] = key
	k.current = id
	return id
}

// CurrentID is the id of the key new values are sealed with.
func (k *Keyring) CurrentID() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.current
}

// IDs lists all key ids in the ring.
func (k *Keyring) IDs() []string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	ids := make([]string, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// WithNewKey returns a copy of the ring with an added, current, new key.
func (k *Keyring) WithNewKey() *Keyring {
	k.mu.RLock()
	defer k.mu.RUnlock()
	n := &Keyring{keys: map[string][]byte{}, Source: k.Source}
	for id, key := range k.keys {
		n.keys[id] = key
	}
	n.addKey()
	return n
}

// OnlyCurrent returns a copy of the ring holding just the current key.
func (k *Keyring) OnlyCurrent() *Keyring {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return &Keyring{current: k.current, keys: map[string][]byte{k.current: k.keys[k.current]}, Source: k.Source}
}

// KeyID returns the id of the key a sealed value was encrypted with.
func KeyID(sealed string) string {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, ":")
	return id
}

// Sealed reports whether s is an encrypted value.
func Sealed(s string) bool { return strings.HasPrefix(s, prefix) }

// Seal encrypts plain, bound to aad (where the value is stored).
func (k *Keyring) Seal(plain []byte, aad string) (string, error) {
	k.mu.RLock()
	id, key := k.current, k.keys[k.current]
	k.mu.RUnlock()
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, plain, []byte(aad))
	return prefix + id + ":" + base64.RawStdEncoding.EncodeToString(out), nil
}

var ErrUnreadable = errors.New("cannot decrypt a stored secret")

// Open decrypts a value sealed with any key in the ring. It never returns
// ciphertext as if it were plaintext.
func (k *Keyring) Open(sealed, aad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, fmt.Errorf("%w: not an encrypted value", ErrUnreadable)
	}
	id, b64, ok := strings.Cut(rest, ":")
	if !ok {
		return nil, fmt.Errorf("%w: malformed value", ErrUnreadable)
	}
	k.mu.RLock()
	key := k.keys[id]
	k.mu.RUnlock()
	if key == nil {
		return nil, fmt.Errorf("%w: it was encrypted with key %s, which is not in the keyring", ErrUnreadable, id)
	}
	raw, err := base64.RawStdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed value", ErrUnreadable)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: malformed value", ErrUnreadable)
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("%w: wrong key, corrupted, or moved from another row", ErrUnreadable)
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("secret keys must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Load reads a keyring file. Like ssh, it refuses files that other users
// can read.
func Load(path string) (*Keyring, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret key file %s is readable by other users (mode %s); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil || f.Version != 1 {
		return nil, fmt.Errorf("secret key file %s is not a Hangar keyring", path)
	}
	k := &Keyring{current: f.Current, keys: map[string][]byte{}, Source: "file " + path}
	for id, b64 := range f.Keys {
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("secret key file %s has a malformed key %s", path, id)
		}
		k.keys[id] = key
	}
	if k.keys[k.current] == nil {
		return nil, fmt.Errorf("secret key file %s has no current key", path)
	}
	return k, nil
}

// Save writes the keyring atomically with mode 0600.
func (k *Keyring) Save(path string) error {
	k.mu.RLock()
	f := file{Version: 1, Current: k.current, Keys: map[string]string{}}
	for id, key := range k.keys {
		f.Keys[id] = base64.StdEncoding.EncodeToString(key)
	}
	k.mu.RUnlock()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hangar-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// FromEnv builds a single-key keyring from a base64 32-byte key, for keys
// supplied by a secret manager through the environment.
func FromEnv(b64 string) (*Keyring, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(key) != 32 {
		return nil, errors.New("HANGAR_SECRET_KEY must be a base64-encoded 32-byte key (e.g. openssl rand -base64 32)")
	}
	// A stable id derived from nothing secret: the key is identified by
	// position, since an env key cannot be rotated in place.
	return &Keyring{current: "env", keys: map[string][]byte{"env": key}, Source: "environment HANGAR_SECRET_KEY"}, nil
}

// Exists reports whether a keyring file exists at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
