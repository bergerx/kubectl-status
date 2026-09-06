# Verifying a Release

Each release publishes signed artifacts and SBOMs. You can verify the integrity, authenticity, and signer identity of any release asset using `cosign` (keyless OIDC verification — no pre-shared keys).

## Artifacts per release

For version `vX.Y.Z`, the GitHub Release includes:

- `status_<OS>_<ARCH>.tar.gz` — the plugin binary archive
- `status_<OS>_<ARCH>.tar.gz.sig` — cosign signature of the archive
- `status_<OS>_<ARCH>.tar.gz.pem` — OIDC certificate used for signing
- `status_<OS>_<ARCH>.tar.gz.sbom.spdx.json` — SPDX SBOM
- `status_X.Y.Z_checksums.txt` — SHA-256 checksums of all archives
- `status_X.Y_Z_checksums.txt.sig` — cosign signature of the checksums file
- `status_X.Y_Z_checksums.txt.pem` — OIDC certificate for the checksums signature

## Verification steps

1. **Download the artifact and its verification files** (example for Linux amd64):

   ```bash
   VERSION=v0.7.24  # replace with desired version
   ARCH=status_linux_amd64.tar.gz
   BASE="https://github.com/bergerx/kubectl-status/releases/download/$VERSION"
   curl -LO "$BASE/$ARCH"
   curl -LO "$BASE/$ARCH.sig"
   curl -LO "$BASE/$ARCH.pem"
   curl -LO "$BASE/status_${VERSION#v}_checksums.txt"
   curl -LO "$BASE/status_${VERSION#v}_checksums.txt.sig"
   curl -LO "$BASE/status_${VERSION#v}_checksums.txt.pem"
   ```

2. **Verify the checksums file's signature and signer identity** (this confirms the release was built by our CI pipeline, not an attacker):

   ```bash
   cosign verify-blob \
     --certificate=status_${VERSION#v}_checksums.txt.pem \
     --signature=status_${VERSION#v}_checksums.txt.sig \
     --certificate-identity=https://github.com/bergerx/kubectl-status/.github/workflows/release.yml@refs/tags/$VERSION \
     --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
     status_${VERSION#v}_checksums.txt
   ```

   Expected output: `Verified OK`

   - `--certificate-identity` pins the GitHub Actions workflow that produced the release (the `@refs/tags/$VERSION` suffix ensures only that exact tag's run is accepted).
   - `--certificate-oidc-issuer` pins GitHub's OIDC issuer.

3. **Verify the binary archive's checksum against the now-trusted checksums file**:

   ```bash
   sha256sum -c --ignore-missing status_${VERSION#v}_checksums.txt
   ```

   Expected output: `status_linux_amd64.tar.gz: OK`

4. **(Optional) Verify the binary archive's standalone signature** — same workflow identity, separate artifact:

   ```bash
   cosign verify-blob \
     --certificate=$ARCH.pem \
     --signature=$ARCH.sig \
     --certificate-identity=https://github.com/bergerx/kubectl-status/.github/workflows/release.yml@refs/tags/$VERSION \
     --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
     $ARCH
   ```

## Verifying via Krew

If you install via `kubectl krew install/upgrade status`, Krew performs its own verification of the plugin manifest (`.krew.yaml`) against the krew-index repository, which is updated by our release pipeline after the GitHub Release is created. The steps above verify the *upstream* GitHub Release artifacts directly, independent of the krew-index supply chain.

## What this proves

| Criterion | What you verified |
|-----------|-------------------|
| **Integrity** | The artifact's bytes match the checksums file, which was signed by the CI workflow. |
| **Authenticity** | The checksums file was signed by a certificate issued to *our* release workflow at *that exact tag*. |
| **Signer identity** | The certificate's `issuer` is GitHub Actions' OIDC provider, and its `subject` is the `release.yml` workflow at `refs/tags/vX.Y.Z` — not a compromised key or forked workflow. |