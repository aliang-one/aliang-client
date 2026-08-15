# Certificate Layout

This project now treats certificates as two separate systems only:

- MITM: local HTTPS interception
- mTLS: outbound Aliang connector authentication

Anything outside these two purposes is considered legacy or sample material.

## Active Certificate Systems

### 1. MITM

Files:

- Runtime `mitm-ca.pem`
- Runtime `mitm-ca.pem.key`

Role:

- This is the only CA used to sign forged server certificates during HTTPS interception.
- The OS trust store must trust this same MITM CA directly.

Code paths:

- `processor/cert/client/client_cert.go`
- `processor/tcp/tls.go`
- `app/http/services/cert_service.go`
- `processor/cert/installer/installer.go`

Important behavior:

- The running process reads MITM cert material from the runtime cache/state directory via `processor/cert/paths.go`.
- If the runtime MITM files are missing, the code generates a fresh self-signed MITM CA.
- If the old system-installed MITM cert remains trusted but the matching private key is gone, interception will fail because new leaf certs will be signed by a different CA.

### 2. Outbound mTLS

Files:

- `processor/cert/client/ca.pem`
- `processor/cert/client/client.crt`
- `processor/cert/client/client.key`

Role:

- `ca.pem` is the trust anchor used to verify the upstream Aliang server.
- `client.crt` and `client.key` are the client-auth certificate and private key presented to the upstream server.

Code paths:

- `processor/cert/client/mtls_client.go`
- `outbound/proxy/aliang/handshaker.go`

Important behavior:

- These files are embedded into the binary.
- They are not the same certificate material as the runtime MITM CA.

## What Was Removed

The project previously had a separate `root-ca` runtime concept alongside `mitm-ca`.
That model has been removed from the active cert configuration because:

- the code did not maintain a stable `root-ca -> mitm-ca` chain
- the UI and runtime behavior already centered on trusting `mitm-ca`
- a single MITM CA better matches the product requirement of uninstalling, regenerating, and reinstalling one local trust anchor

As a result:

- `root-ca` is no longer a supported cert type
- runtime `root-ca.pem` paths are no longer part of active cert resolution

## Remaining Runtime-Managed Names

- `mitm-ca.pem` / `mitm-ca.pem.key`
  Used by local MITM.

- `mtls-client.pem` / `mtls-client.pem.key`
  Still exists as a cert-service export/install naming path, but it is not the same material used by the outbound mTLS connector, which relies on embedded files under `processor/cert/client`.

## Repository Files That Matter

### `processor/cert/client`

- `ca.pem`
  Root CA for outbound mTLS verification.

- `client.crt`
  mTLS client certificate.
  EKU: `TLS Web Client Authentication`.

- `client.key`
  Private key for `client.crt`.

- `mitm-ca.pem`
  Sample MITM signing CA checked into the repository.
  Useful as reference material, but normal runtime MITM uses the state-directory copy instead of this checked-in file.

- `mitm-ca.key.pem`
  Private key for the checked-in sample MITM CA.

- `mtls_client.go`
  Embeds the outbound mTLS certificate material.

- `client_cert.go`
  Loads and manages runtime MITM cert material.

- `mitm-ca.mobileconfig`
  Apple profile template for installing the MITM CA.

- `openssl-gen.sh` and `openssl.cnf`
  Helper files related to the sample certificate chain in this directory.

## Key Separation Rules

- MITM and mTLS are different roles and should stay separate.
- MITM acts as a local server certificate issuer for intercepted traffic.
- mTLS acts as a client certificate when dialing the upstream Aliang server.
- `processor/cert/client/ca.pem` should not be treated as the active runtime MITM trust anchor unless the code is explicitly redesigned to do that.

## Debugging Checklist

1. MITM fails:
   Check whether the OS trusts the same `mitm-ca.pem` whose private key is available in the runtime state directory.

2. Outbound Aliang mTLS fails:
   Check `processor/cert/client/mtls_client.go` and verify the embedded `ca.pem`, `client.crt`, and `client.key` still match the upstream environment.

3. Reinstall behavior breaks interception:
   Check whether the app deleted local `mitm-ca.pem.key` while the OS still trusts an older MITM CA.
