// Package secrets stores connection passwords in the OS keychain (macOS
// Keychain, Linux Secret Service, Windows Credential Manager).
package secrets

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

const service = "squi"

// Kind distinguishes the secrets stored per connection.
type Kind string

const (
	DBPassword    Kind = "db"
	SSHPassword   Kind = "ssh"
	SSHPassphrase Kind = "passphrase"
)

// Store is the secret storage backend.
type Store interface {
	Get(connID string, k Kind) (string, bool, error)
	Set(connID string, k Kind, value string) error
	Delete(connID string, k Kind) error
}

// Keyring is the OS keychain backend.
type Keyring struct{}

func key(connID string, k Kind) string { return connID + "/" + string(k) }

// Get returns the secret and whether it exists.
func (Keyring) Get(connID string, k Kind) (string, bool, error) {
	v, err := keyring.Get(service, key(connID, k))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set stores a secret.
func (Keyring) Set(connID string, k Kind, value string) error {
	return keyring.Set(service, key(connID, k), value)
}

// Delete removes a secret; missing secrets are not an error.
func (Keyring) Delete(connID string, k Kind) error {
	err := keyring.Delete(service, key(connID, k))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// DeleteAll removes every secret of a connection.
func DeleteAll(s Store, connID string) error {
	var errs []error
	for _, k := range []Kind{DBPassword, SSHPassword, SSHPassphrase} {
		if err := s.Delete(connID, k); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Memory is an in-memory store for tests and for sessions where the
// keychain is unavailable.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *Memory) Get(connID string, k Kind) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[key(connID, k)]
	return v, ok, nil
}

func (m *Memory) Set(connID string, k Kind, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[key(connID, k)] = value
	return nil
}

func (m *Memory) Delete(connID string, k Kind) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key(connID, k))
	return nil
}
