# Voxa server credential security

How the Go server (`server/`) protects provider credentials (API keys for
transcription/summary providers) at rest. Operating instructions are in
`server/README.md`, and persistence design is in `docs/server-persistence.md`.

## Summary

| Property | Value |
| --- | --- |
| Algorithm | AES-256-GCM (Go standard library `crypto/aes` + `crypto/cipher`) |
| Key size | 32 bytes (256 bits), supplied as standard padded base64 |
| Nonce | 12 bytes from `crypto/rand`, fresh for every encryption |
| Tag | 16 bytes, appended to the ciphertext |
| AAD | length-prefixed `voxa.credential.v1`, workspace ID, provider, key version |
| Storage | `workspace_credentials` in PostgreSQL: ciphertext, nonce, key version |
| Key location | process environment only; never in PostgreSQL |
| Plaintext reads over HTTP | none; GET returns provider, configured state and a fixed mask |

## Encryption

`internal/credentialcrypto.Cipher.Seal` takes the active key version from
the `KeyRing`, generates a fresh random 96-bit nonce, and computes
`AES-256-GCM.Seal(nonce, secret, AAD)`. Encryption is never deterministic:
encrypting the same secret twice gives a different nonce and a different
ciphertext. Every `PUT` re-encrypts with a new nonce.

With random 96-bit nonces, NIST SP 800-38D advises at most 2^32
encryptions per key. Credential writes are many orders of magnitude below
that. If write volume ever approached it, rotate keys (see below) long
before then.

`Open` decrypts with the key named by the row's stored `key_version`. Every
failure maps to one of three typed errors, none of which carries key or
plaintext material:

- `ErrUnknownKeyVersion`: the key ring has no key for that version.
- `ErrMalformed`: wrong nonce length, a ciphertext shorter than the tag, or
  an invalid version string.
- `ErrDecrypt`: authentication failed because of a wrong key, a modified
  ciphertext/nonce/tag, or a mismatched AAD.

## Additional authenticated data

The AAD binds each ciphertext to its logical identity. It is the
concatenation, in this order, of these four fields, each encoded as a
**4-byte big-endian unsigned length followed by the raw bytes**:

1. `voxa.credential.v1` (record-format label)
2. workspace ID
3. provider
4. key version

Example for workspace `ws`, provider `openai`, version `v1`:

```text
00000012 "voxa.credential.v1" 00000002 "ws" 00000006 "openai" 00000002 "v1"
```

Length prefixes make the encoding unambiguous (`("ab","c")` and `("a","bc")`
differ). A ciphertext copied to another workspace or provider, or relabeled
with another key version, fails authentication. Integration tests do this
with SQL and assert `ErrDecrypt`. The label lets a future record format
coexist without reinterpreting old rows.

## Schema (`0002_workspace_credentials.sql`)

```text
workspace_credentials
  workspace_id  text  FK -> workspaces, part of the primary key
  provider      text  part of the primary key, CHECK same pattern the API enforces
  key_version   text  CHECK [A-Za-z0-9][A-Za-z0-9._-]{0,31}, indexed
  nonce         bytea CHECK length = 12
  ciphertext    bytea CHECK length > 16 (secret + 16-byte tag)
  created_at, updated_at
```

There is no plaintext column and no stored mask. The `••••` mask is
response presentation, added by the API.

## Key ring and configuration

`credentialcrypto.KeyRing` is the seam between encryption and key sourcing:

```go
type KeyRing interface {
    ActiveVersion() string
    AEAD(ctx context.Context, version string) (cipher.AEAD, error)
}
```

`StaticKeyRing` is built at startup from the environment:

| Variable | Meaning |
| --- | --- |
| `VOXA_CREDENTIAL_KEY` | Active key: base64 (standard, padded) of exactly 32 bytes. |
| `VOXA_CREDENTIAL_KEY_VERSION` | Identifier written to every row sealed with the active key. |
| `VOXA_CREDENTIAL_PREVIOUS_KEYS` | Optional decrypt-only keys: `version:base64,version:base64`. |

The server validates these **before** connecting to PostgreSQL. If any is
missing or malformed it exits non-zero, so it never falls back to plaintext
storage and never reports ready. Passphrases, hex, unpadded or URL-safe
base64, and lengths other than 32 bytes are rejected. Error messages name
the variable (and the entry index for previous keys) but never echo the
value. Decoded key bytes are zeroed after the AES key schedules are built,
on a best-effort basis.

Key versions are **non-secret metadata**: they are stored in plaintext beside
each row and appear in logs (`active_key_version`, `loaded_key_versions`).
Do not put secrets in version names.

### Future KMS / Vault key rings

A `KeyRing` backed by a managed secret system can replace `StaticKeyRing`
without changing stored records:

- **Envelope keys:** at startup, decrypt per-version data keys with AWS KMS,
  GCP KMS, Azure Key Vault, or Vault Transit, and hold only those data keys
  in memory. The `AEAD(ctx, version)` method returns the cipher for a
  version.
- **Remote encryption:** an implementation can also call the remote service
  per operation. The `ctx` parameter exists for that.

## Rotation

Rows record the key version that sealed them, so rotation needs no schema
change. Writes always use the active version, and reads use the row's
version.

1. Generate a new key. Set it as `VOXA_CREDENTIAL_KEY` with a new
   `VOXA_CREDENTIAL_KEY_VERSION`, and move the old pair into
   `VOXA_CREDENTIAL_PREVIOUS_KEYS`.
2. Restart. The log shows the new `active_key_version` and both loaded
   versions.
3. New and updated credentials are sealed with the new key. Existing rows
   stay readable with the old key.
4. *(Future task.)* A re-encryption job opens each row still on the old
   version and reseals it with the active key. Rows to process:
   `SELECT workspace_id, provider FROM workspace_credentials WHERE key_version = 'old'`.
5. Retire the old key only once
   `SELECT count(*) FROM workspace_credentials WHERE key_version = 'old'` is
   zero.

If a stored row references a version that is not loaded, startup logs a
warning with that version and its row count. Metadata reads keep working,
but those rows cannot be decrypted until the key is supplied.

Automatic bulk re-encryption is intentionally not implemented yet.

## Access boundaries

- `repository.CredentialRepository` (used by the HTTP-facing
  `CredentialService`) only lists metadata, checks configured state, writes
  and deletes. None of its methods decrypt.
- `repository.CredentialSecretReader.RevealCredential` is the only
  decryption path. It is a separate interface that no HTTP handler or
  service depends on. A future provider runtime would use it, and should
  `Wipe()` the secret after the provider call.
- `domain.CredentialSecret` wraps raw bytes. `String`, `GoString`, `Format`
  (every verb), `LogValue` and `MarshalJSON` all print `[REDACTED]`. The
  `PUT` body decodes directly into it, so no plain string field holds the
  value. This is accident prevention, not secure memory: Go may copy
  bytes, and the raw request body is briefly in memory during decoding.

## Logging rules

The server never logs credential values, request bodies, headers (including
`Authorization`), query strings, encryption keys, ciphertexts, nonces, the
database URL, or SQL parameters. Failure logs (`msg: "request failed"`)
contain the request ID, method, route template, operation, public error
code, error category, workspace ID, provider and a sanitized error chain.
Decryption failures carry a `CredentialUnreadableError` that logs only
workspace, provider, key version and the typed reason. HTTP tests send a
canary secret and fake database password through success, validation,
internal and outage paths, and assert that neither appears in any body or
log.

## Threat model

**Application-level encryption protects against:**

- disclosure of plaintext credentials from database dumps, backups,
  replicas, snapshots, or read-only SQL access;
- a database operator or DBA who does not hold the master key;
- ciphertext tampering or relocation between workspaces, providers or key
  versions, which GCM authentication and the AAD detect;
- accidental exposure through API responses and logs, through the redacting
  type and the metadata-only read paths.

**It does NOT protect against:**

- a compromised running Voxa server, or anyone who can read its environment
  or memory. That process holds the master key and can decrypt every
  credential;
- an attacker with both a database copy and the master key;
- an attacker with database **write** access deleting credential rows or
  replacing them with their own validly encrypted values. Write access
  needs the key to forge rows, but deleting rows needs no key;
- missing transport security. **TLS is still required in production**
  between clients and the server, and between the server and PostgreSQL
  (`sslmode=verify-full`);
- an authenticated caller abusing write access. There is no authentication
  or workspace membership yet. Today anyone who can reach the API can
  overwrite or delete credentials for the development workspace (but never
  read them).

## Production key management

The environment-variable key ring is suitable for development and simple
deployments. Production should source key material from a managed secret
system:

- AWS KMS / Secrets Manager
- GCP Cloud KMS / Secret Manager
- Azure Key Vault
- HashiCorp Vault (Transit or KV)
- Kubernetes Secrets populated by an external secret manager, never
  committed manifests

Keep keys out of source control, images, compose files and shell history.
Restrict who can read the server's environment. Back up keys separately
from database backups: losing a key makes the credentials sealed with it
unrecoverable, and storing the key next to the backup defeats the
encryption.

## Generating a development key

```sh
export VOXA_CREDENTIAL_KEY="$(openssl rand -base64 32)"
export VOXA_CREDENTIAL_KEY_VERSION=dev-1
```

Without OpenSSL:
`head -c 32 /dev/urandom | base64` (output must be one line).

Never reuse a documented or shared key. The repository deliberately
contains no key material.
