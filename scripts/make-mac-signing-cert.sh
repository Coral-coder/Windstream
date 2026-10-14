#!/bin/sh
# Creates the self-signed certificate the release build signs Windstream.app
# with, and prints the two values to store as GitHub Actions secrets:
#
#   MACOS_CERTIFICATE_P12       the certificate and its private key (base64)
#   MACOS_CERTIFICATE_PASSWORD  the password protecting them
#
# A self-signed certificate does not make macOS trust the download (that
# takes an Apple Developer ID and notarization), but it gives every release
# the same identity, so the Screen Recording and Accessibility permissions
# survive updates. Keep the files private: anyone holding the key can make
# apps macOS takes for Windstream. Make it once; a new certificate means
# allowing the permissions again on every Mac.
#
# Usage: scripts/make-mac-signing-cert.sh [output-dir]
set -eu
out=${1:-windstream-signing}
mkdir -p "$out"
umask 077
cat > "$out/cert.cnf" <<'CNF'
[req]
distinguished_name = dn
prompt = no
x509_extensions = ext
[dn]
CN = Windstream Self-Signed
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
subjectKeyIdentifier = hash
CNF
password=$(openssl rand -hex 24)
openssl req -x509 -newkey rsa:3072 -nodes -keyout "$out/key.pem" -out "$out/cert.pem" \
  -days 7300 -config "$out/cert.cnf" 2>/dev/null
# 3DES/SHA-1 protection: the formats macOS's `security import` reads.
openssl pkcs12 -export -inkey "$out/key.pem" -in "$out/cert.pem" -out "$out/cert.p12" \
  -name "Windstream Self-Signed" -passout "pass:$password" \
  -keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1
openssl base64 -A -in "$out/cert.p12" > "$out/MACOS_CERTIFICATE_P12.txt"
printf '%s' "$password" > "$out/MACOS_CERTIFICATE_PASSWORD.txt"
rm -f "$out/key.pem" "$out/cert.cnf"
echo "Wrote $out/MACOS_CERTIFICATE_P12.txt and $out/MACOS_CERTIFICATE_PASSWORD.txt"
echo "Certificate SHA-1: $(openssl x509 -in "$out/cert.pem" -noout -fingerprint -sha1 | cut -d= -f2 | tr -d :)"
