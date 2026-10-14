#!/bin/sh
# Imports the signing certificate from the MACOS_CERTIFICATE_P12 and
# MACOS_CERTIFICATE_PASSWORD secrets into a temporary keychain and exports
# SIGN_ID (its SHA-1), SIGN_KEYCHAIN and SIGN_KIND (developer-id or
# self-signed) for the following steps. Without the secret it exports
# SIGN_ID=- (ad-hoc signing).
set -eu
env_out=${GITHUB_ENV:-/dev/stdout}
if [ -z "${MACOS_CERTIFICATE_P12:-}" ]; then
  echo "::warning::No MACOS_CERTIFICATE_P12 secret: signing ad-hoc, so macOS asks for Screen Recording and Accessibility again after every update. See scripts/make-mac-signing-cert.sh."
  echo "SIGN_ID=-" >> "$env_out"
  echo "SIGN_KIND=adhoc" >> "$env_out"
  exit 0
fi
dir=${RUNNER_TEMP:-$(mktemp -d)}
keychain="$dir/windstream-signing.keychain-db"
kcpass=$(openssl rand -hex 16)
printf '%s' "$MACOS_CERTIFICATE_P12" | base64 --decode > "$dir/cert.p12"
security create-keychain -p "$kcpass" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$kcpass" "$keychain"
security import "$dir/cert.p12" -k "$keychain" -P "${MACOS_CERTIFICATE_PASSWORD:-}" -T /usr/bin/codesign >/dev/null
rm -f "$dir/cert.p12"
security set-key-partition-list -S apple-tool:,apple: -s -k "$kcpass" "$keychain" >/dev/null
# codesign looks identities up in the search list.
security list-keychains -d user -s "$keychain" $(security list-keychains -d user | tr -d '"')
line=$(security find-identity -p codesigning "$keychain" | awk '/[0-9A-F]{40}/{print; exit}')
id=$(echo "$line" | awk '{print $2}')
name=$(echo "$line" | sed 's/^[^"]*"//; s/"[^"]*$//')
if [ -z "$id" ]; then
  echo "::error::MACOS_CERTIFICATE_P12 holds no code signing identity"
  exit 1
fi
kind=self-signed
case "$name" in "Developer ID Application:"*) kind=developer-id ;; esac
echo "Signing as \"$name\" ($id, $kind)"
echo "SIGN_ID=$id" >> "$env_out"
echo "SIGN_KEYCHAIN=$keychain" >> "$env_out"
echo "SIGN_KIND=$kind" >> "$env_out"
