# The machine a number came from, printed by every lane that measures time.
#
# "An M-series laptop" covered an M1 Max with 10 cores and an M4 Max with 16,
# and the goroutine lane reads about twice as fast on the second. A timing
# without its host is not reproducible and not comparable, including against
# itself a month later.
#
# Sourced, not run: `. "$(dirname "$0")/../host.sh"; host`
host() {
  local cpu cores os
  if [ "$(uname -s)" = Darwin ]; then
    cpu=$(sysctl -n machdep.cpu.brand_string 2>/dev/null)
    cores=$(sysctl -n hw.ncpu 2>/dev/null)
    os="macOS $(sw_vers -productVersion 2>/dev/null)"
  else
    cpu=$(awk -F': ' '/model name/ {print $2; exit}' /proc/cpuinfo 2>/dev/null)
    cores=$(nproc 2>/dev/null)
    os=$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")
  fi
  printf 'host: %s · %s cores · %s · %s\n\n' "${cpu:-unknown}" "${cores:-?}" "$(uname -m)" "${os:-$(uname -s)}"
}
