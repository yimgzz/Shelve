// Package vault implements the encrypted credential store:
// Argon2id KDF (64 MiB, t=3, p=4, 16 B salt) + AES-256-GCM envelope
// (AAD "dsmsv1"), first-run creation, unlock, debounced-free Save and
// lock with key zeroization (master plan §2 D3, §4, §8).
package vault
