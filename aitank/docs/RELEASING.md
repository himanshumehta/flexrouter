# Releasing aitank

Releases are built by `.github/workflows/aitank-release.yml` on a macOS
runner when a tag like `aitank-v0.1.0` is pushed. It builds an arm64 + amd64
universal binary, signs and notarizes it, signs the checksums for
`aitank update`, publishes a GitHub release and updates the Homebrew tap.
Each optional step is skipped when its secret is missing, so the workflow
runs end to end before the Apple credentials exist.

## One-time setup (repository settings → Secrets and variables → Actions)

| Name | Kind | What it is |
|---|---|---|
| `AITANK_SIGNING_KEY` | secret | ed25519 private key for checksums; make it with `go run ./tools/relsign genkey` |
| `AITANK_UPDATE_PUBKEY` | variable | the matching public key; compiled into the binary so `aitank update` can verify downloads |
| `MACOS_CERT_P12` | secret | base64 of a "Developer ID Application" certificate exported as .p12 |
| `MACOS_CERT_PASSWORD` | secret | the .p12 password |
| `MACOS_SIGN_IDENTITY` | secret | e.g. `Developer ID Application: Your Name (TEAMID)` |
| `NOTARY_KEY` | secret | base64 of an App Store Connect API key (.p8) with Developer access |
| `NOTARY_KEY_ID` | secret | that key's ID |
| `NOTARY_ISSUER` | secret | the issuer ID from App Store Connect → Users and Access → Integrations |
| `HOMEBREW_TAP_TOKEN` | secret | a fine-grained token with contents:write on `himanshumehta/homebrew-tap` |

Create the tap repository `himanshumehta/homebrew-tap` (empty is fine). Users
then install with `brew install himanshumehta/tap/aitank`. Getting into
homebrew-core (`brew install aitank` with no tap) needs a separate submission
once the project has users and a stable release history.

## Cutting a release

1. Move the `## x.y.z (unreleased)` heading in `CHANGELOG.md` to the version
   and date.
2. `git tag aitank-vX.Y.Z && git push origin aitank-vX.Y.Z`
3. Check the workflow run, then on a Mac:
   `spctl -a -vv -t install $(which aitank)` should say "Notarized Developer ID".

Tags must start with `aitank-v` so they never trigger flexrouter's PyPI
workflow (`v*.*.*`).
