package sshx

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"

	"shelve/internal/model"
)

// ErrKeyPassphraseRequired is returned when the key file is
// passphrase-encrypted and no passphrase was supplied. The 3c engine
// matches it with errors.Is to show the vault:key-prompt modal (one
// in-flight prompt per connection, master plan A2).
type ErrKeyPassphraseRequired struct {
	KeyPath string
}

func (e *ErrKeyPassphraseRequired) Error() string {
	return fmt.Sprintf("ssh key file requires a passphrase: %s", e.KeyPath)
}

// ErrKeyPassphraseWrong is returned when the supplied passphrase does
// not decrypt the key file. The engine may re-prompt (master plan A2).
var ErrKeyPassphraseWrong = errors.New("ssh key file passphrase is incorrect")

// ErrKeyFormat is returned when the key file is not a parseable
// private key (PEM/OpenSSH; RSA, ECDSA, Ed25519, DSA). Detail carries
// the parser's own message; it never contains key material.
type ErrKeyFormat struct {
	Path   string
	Detail string
}

func (e *ErrKeyFormat) Error() string {
	return fmt.Sprintf("ssh key file is not a supported private key: %s (%s)", e.Path, e.Detail)
}

// AuthMethods converts a stored credential (master plan §4) into the
// SSH auth-method list the 3c engine hands to ssh.Dial.
//
// keyPassphrase is the key-file passphrase entered at connect time and
// cached in process memory for the app session; it is never stored
// (master plan A2). A nil keyPassphrase is fine for unencrypted keys
// and yields *ErrKeyPassphraseRequired for encrypted ones.
func AuthMethods(auth model.Auth, keyPassphrase *string) ([]ssh.AuthMethod, error) {
	switch auth.Type {
	case model.AuthPassword:
		return []ssh.AuthMethod{ssh.Password(auth.Password)}, nil
	case model.AuthKey:
		signer, err := parseKeyFile(auth.KeyPath, keyPassphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("sshx: unsupported auth type %d (password XOR key)", int(auth.Type))
	}
}

// parseKeyFile reads and parses the private-key file at keyPath
// (read-only, master plan §8.6) into an ssh signer, with the typed
// errors the prompt flow relies on:
//   - missing file            -> "ssh key file not found: <path>"
//   - unreadable file         -> "ssh key file not readable: <path>"
//   - unparseable file        -> *ErrKeyFormat
//   - encrypted, no passphrase-> *ErrKeyPassphraseRequired
//   - wrong passphrase        -> ErrKeyPassphraseWrong
func parseKeyFile(keyPath string, passphrase *string) (ssh.Signer, error) {
	data, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("ssh key file not found: %s", keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("ssh key file not readable: %s", keyPath)
	}

	if signer, err := ssh.ParsePrivateKey(data); err == nil {
		// Unencrypted key; a supplied passphrase is simply unused.
		return signer, nil
	} else {
		var missing *ssh.PassphraseMissingError
		if !errors.As(err, &missing) {
			return nil, &ErrKeyFormat{Path: keyPath, Detail: err.Error()}
		}
		if passphrase == nil {
			return nil, &ErrKeyPassphraseRequired{KeyPath: keyPath}
		}
	}

	signer, err := ssh.ParsePrivateKeyWithPassphrase(data, []byte(*passphrase))
	if err != nil {
		// x/crypto reports a failed decrypt of an encrypted key as
		// x509.IncorrectPasswordError; anything else is a corrupt file.
		if !errors.Is(err, x509.IncorrectPasswordError) {
			return nil, &ErrKeyFormat{Path: keyPath, Detail: err.Error()}
		}
		return nil, ErrKeyPassphraseWrong
	}
	return signer, nil
}
