set -eu
cd /w/dist
sig() { curl -fsS --aws-sigv4 "aws:amz:$S3_REGION:s3" -u "$S3_ADMIN_ACCESS_KEY:$S3_ADMIN_SECRET_KEY" "$@"; }
# b64 <hex sha256> -> the base64 digest S3 checksums speak, in POSIX sh + awk.
b64() { printf '%b' "$(printf '%s' "$1" | awk '{h="0123456789abcdef"; for(i=1;i<length($0);i+=2) printf "\\0%o", (index(h,substr($0,i,1))-1)*16+index(h,substr($0,i+1,1))-1}')" | base64; }
put() { sig -X PUT -H 'Content-Type: application/octet-stream' -H "x-amz-checksum-sha256: $(b64 "$2")" --data-binary @"$1" "$PUT_BASE/$1" >/dev/null; }
: > /w/sums.txt
idx=""; n=0
for f in *; do
  [ -f "$f" ] || continue
  sha="$(sha256sum "$f" | cut -d' ' -f1)"
  meta="$(grep -m1 "^$f	" /w/meta.txt || true)"
  os="$(printf '%s' "$meta" | cut -f2)"; arch="$(printf '%s' "$meta" | cut -f3)"
  name="$(printf '%s' "$meta" | cut -f4)"
  put "$f" "$sha"
  printf '%s %s\n' "$f" "$sha" >> /w/sums.txt
  echo "published $BASE/$f  $sha"
  idx="$idx${idx:+,}{\"name\":\"${name:-$f}\",\"os\":\"${os:-any}\",\"arch\":\"${arch:-any}\",\"url\":\"$BASE/$f\",\"sha256\":\"$sha\"}"
  n=$((n+1))
done
[ "$n" -gt 0 ] || { echo 'no artifacts to publish'; exit 1; }
printf '{"repo":"%s","tag":"%s","binaries":[%s]}\n' "$REPO" "$TAG" "$idx" > binaries.json
put binaries.json "$(sha256sum binaries.json | cut -d' ' -f1)"
echo "index $BASE/binaries.json"
sig -o /w/readback "$PUT_BASE/binaries.json" || { echo "read-back: binaries.json is missing or unreadable"; exit 1; }
[ "$(sha256sum < /w/readback)" = "$(sha256sum < binaries.json)" ] || { echo "read-back: binaries.json holds other bytes than were written"; exit 1; }
rm -f /w/readback
while read -r f sha; do
  h="$(sig -I -H 'x-amz-checksum-mode: ENABLED' "$PUT_BASE/$f")" || { echo "read-back: $f is missing or unreadable"; exit 1; }
  got="$(printf '%s\n' "$h" | tr -d '\r' | sed -n 's/^[Xx]-[Aa]mz-[Cc]hecksum-[Ss]ha256: *//p')"
  [ "$got" = "$(b64 "$sha")" ] || { echo "read-back: $f is stored with sha256 ${got:-none}, the index says $(b64 "$sha")"; exit 1; }
done < /w/sums.txt
echo "read back binaries.json, and the store's sha256 for $n artifacts: every one matches the index"
echo "no host read path yet: the bucket is private and hosts fetch the index and binaries without a credential"
cat binaries.json
