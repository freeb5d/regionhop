#!/usr/bin/env bash
# Traffic liveness probe, run periodically (psi-healthcheck.timer) as the
# unpriviliged psipanel user. Proves whether each location's tunnel is
# actually passing traffic, independent of whether any real client happened
# to use it recently — TotalBytesTransferred in the tunnel's own journal
# only reflects that, so a tunnel with no organic traffic looks identical to
# a broken one there. This probes every location itself instead of waiting
# for one.
#
# Rewrites $PREFIX/data/health.json from scratch on every run (atomically,
# via a temp file + rename) with the current set of locations only, so a
# removed location's stale result doesn't linger forever.
set -u

PREFIX=/opt/psi-panel
CONFIGS_DIR="$PREFIX/configs"
OUT="$PREFIX/data/health.json"
TMP="$OUT.tmp.$$"

echo "{" > "$TMP"
first=1
shopt -s nullglob
for cfg in "$CONFIGS_DIR"/*.json; do
  name=$(basename "$cfg" .json)
  port=$(grep -oP '"LocalSocksProxyPort"\s*:\s*\K[0-9]+' "$cfg" 2>/dev/null || true)
  [[ -n "$port" ]] || continue

  start=$(date +%s%N)
  http_code=$(curl -s -o /dev/null -w '%{http_code}' \
    --max-time 15 \
    -x "socks5h://127.0.0.1:${port}" \
    https://example.com/ 2>/dev/null)
  end=$(date +%s%N)
  elapsed_ms=$(( (end - start) / 1000000 ))

  result="fail"
  [[ "$http_code" == "200" ]] && result="ok"

  [[ $first -eq 1 ]] || echo "," >> "$TMP"
  first=0
  printf '"%s":{"result":"%s","time":"%s","elapsedMs":%d}' \
    "$name" "$result" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$elapsed_ms" >> "$TMP"
done
echo "}" >> "$TMP"

mv -f "$TMP" "$OUT"
