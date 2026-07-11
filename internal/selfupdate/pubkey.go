package selfupdate

// trustedPublicKeyB64 is the base64 (std encoding) ed25519 public key whose
// matching private key signs release manifests. Generate the pair with
// `make genkey`: it prints the public key to paste here and a private key you
// store ONLY as the ATL_RELEASE_PRIVATE_KEY GitHub Actions secret — never in
// the repo.
//
// Fail-closed: while this is empty the CLI performs NO auto-update. It will
// never apply an unsigned or unverifiable update, so a compromised release
// (swapped binary AND hash) still cannot push code to users without the private
// key. Manual installation via the published install.sh remains available.
const trustedPublicKeyB64 = "YsNcb89P4YVw0ru2sflGVa7Pm59UZlPSQyEJ+vMG0kY="
