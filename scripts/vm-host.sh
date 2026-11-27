#!/usr/bin/env bash
# vm-host.sh — what hanzo-vm needs from this host before `make start` boots it.
#
#   make vm-host           check, and name what is missing
#   make vm-host-install   install the pinned cloud-hypervisor into ~/.local/bin
#
# On Linux x86-64 hanzo-vm drives a cloud-hypervisor child process, which it
# looks up on PATH and then in ~/.local/bin. With none it answers
# "Virtualization is not supported on this machine": the same words it has for
# a host with no KVM, because its one test is `/dev/kvm exists && the binary is
# found` (hanzoai/vm crates/vm-ch `supported`). Neither the hanzo CLI nor
# hanzo-vm's own installer ships the binary, so a host that passes every KVM
# check still fails at "creating the k3s checkpoint". Linux arm64 (KVM
# directly) and macOS (Virtualization.framework) need no helper.
#
# virtiofsd is the other helper hanzo-vm names, and it is started only for a
# `--mount`. `hanzo up` mounts nothing, so it is not asked for here.
set -euo pipefail

# The release hanzoai/vm is verified with. The digest is the release asset's
# own (cloud-hypervisor-static); a different version needs its digest too.
VERSION="${CLOUD_HYPERVISOR_VERSION:-v53.0}"
SHA256="${CLOUD_HYPERVISOR_SHA256:-448af3d4e59b22c2987f7df94c213ad40fb53a10d437e42b5ee6c4fce7c29ecc}"
DIR="$HOME/.local/bin"
BIN="$DIR/cloud-hypervisor"

# The helper is hanzo-vm's on this one platform only.
needs_helper() { [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ]; }

# hanzo-vm's own lookup order: PATH, then ~/.local/bin.
found() {
  command -v cloud-hypervisor 2>/dev/null && return 0
  [ -x "$BIN" ] && echo "$BIN"
}

check() {
  [ "$(uname -s)" = Linux ] || return 0
  if [ ! -e /dev/kvm ]; then
    echo "hanzo-vm needs KVM and /dev/kvm does not exist: enable VT-x/AMD-V in firmware and load kvm_intel or kvm_amd." >&2
    return 1
  fi
  if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
    echo "hanzo-vm needs read/write on /dev/kvm: sudo usermod -aG kvm $(id -un), then sign in again." >&2
    return 1
  fi
  needs_helper || return 0
  if ! found >/dev/null; then
    cat >&2 <<EOF
hanzo-vm needs cloud-hypervisor on Linux x86-64 and none is on PATH or in $DIR.
Without it the boot fails as "Virtualization is not supported on this machine",
though KVM here is fine. Install the pinned $VERSION release with:

    make vm-host-install
EOF
    return 1
  fi
}

install() {
  if ! needs_helper; then
    echo "hanzo-vm needs no helper binary on $(uname -s) $(uname -m)."
    return 0
  fi
  local have
  if have="$(found)"; then
    echo "cloud-hypervisor is already installed: $have"
    return 0
  fi
  local url="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$VERSION/cloud-hypervisor-static"
  mkdir -p "$DIR"
  # Beside its destination, so the rename is atomic and a refused download
  # never leaves a binary hanzo-vm would find.
  # The trap runs at exit, after this function's locals are gone, so the path
  # is written into it now rather than read from a variable then.
  local tmp
  tmp="$(mktemp "$DIR/.cloud-hypervisor.XXXXXX")"
  trap "rm -f -- '$tmp'" EXIT
  echo "installing cloud-hypervisor $VERSION → $BIN"
  curl -fsSL -o "$tmp" "$url"
  if ! echo "$SHA256  $tmp" | sha256sum --check --status; then
    echo "cloud-hypervisor $VERSION does not match its pinned sha256 ($SHA256); nothing was installed." >&2
    return 1
  fi
  chmod 755 "$tmp"
  mv "$tmp" "$BIN"
  case ":${PATH:-}:" in
    *":$DIR:"*) ;;
    *) echo "note: $DIR is not in PATH (hanzo-vm checks $DIR automatically; add it to PATH to invoke cloud-hypervisor directly)." ;;
  esac
  check
}

case "${1:-check}" in
  check) check ;;
  install) install ;;
  *) echo "usage: $0 [check|install]" >&2; exit 2 ;;
esac
