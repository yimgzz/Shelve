// Package vault implements the encrypted credential store:
// Argon2id KDF (64 MiB, t=3, p=4, 16 B salt) + AES-256-GCM envelope
// (AAD "dsmsv1"), first-run creation, unlock, lock and key zeroization
// (master plan §2 D3, §8).
//
// Populated in Phase 2.
package vault
