#!/usr/bin/env bash
# publish.sh <pr-number> <dir>      — post <dir>/block.md as the validation block on a PR
# publish.sh --upload-only <dir>    — upload and print the rewritten block; touch no PR
#
# <dir> holds block.md — bounded by the validation markers, referencing every file as
# ./<name> — and the PNGs (and MP4s) it references. Run it from the repository's checkout,
# or set GH_REPO=<owner>/<repo>.
#
# Where the files go is decided by the environment, not by a flag:
#
#   QA_R2_ENDPOINT QA_R2_BUCKET QA_R2_PUBLIC_URL QA_R2_ACCESS_KEY_ID QA_R2_SECRET_ACCESS_KEY
#     all set  → each file is PUT to the R2 bucket under <repo>/<pr>/<random>/<name>, and its
#                ./ reference becomes <public url>/<that key>. The random segment makes the
#                path unguessable and keeps a re-run from overwriting the previous one.
#     none set → `gh pr edit --attach`, GitHub's user attachments, as before. A deployment
#                running this skill without a bucket keeps working.
#     some set → refused: a half-configured bucket is a mistake to report, not to paper over.
#
# Uploads are signed by curl itself (--aws-sigv4, curl 7.75+): the runtime image has curl and
# jq and no Python, and nothing here needs more.
set -euo pipefail

die() { echo "publish.sh: $*" >&2; exit 1; }

# Caps on what one run may put in the bucket. A screenshot is a few hundred kB; anything near
# these is a loop gone wrong, and stopping it is cheaper than paying to store it.
max_file=$((10 * 1024 * 1024))
max_total=$((100 * 1024 * 1024))
max_files=40

mode=${1:?usage: publish.sh <pr-number>|--upload-only <dir>}
dir=$(cd "${2:?usage: publish.sh <pr-number>|--upload-only <dir>}" && pwd)
block="$dir/block.md"
start='<!-- validation:start -->'
end='<!-- validation:end -->'

if [ "$mode" = --upload-only ]; then
  pr=""
else
  [[ $mode =~ ^[0-9]+$ ]] || die "first argument must be a PR number or --upload-only, not '$mode'"
  pr=$mode
fi

[ -f "$block" ] || die "missing $block"
grep -qF "$start" "$block" && grep -qF "$end" "$block" ||
  die "block.md must be wrapped in '$start' … '$end'"

# The files are exactly the ones block.md references.
mapfile -t refs < <(grep -oE '\./[A-Za-z0-9._-]+\.[A-Za-z0-9]+' "$block" | sed 's#^\./##' | sort -u)
[ "${#refs[@]}" -gt 0 ] || die "block.md references no files"
[ "${#refs[@]}" -le "$max_files" ] || die "${#refs[@]} files referenced; the cap is $max_files"
for r in "${refs[@]}"; do
  [ -f "$dir/$r" ] || die "block.md references ./$r, which is not in $dir"
  case $r in *.png|*.mp4) ;; *) die "./$r: only .png and .mp4 are published" ;; esac
done

# The browser's screencast writes VP9 even into an .mp4, and Safari — every reviewer on an
# iPhone — will not play that. H.264 plays everywhere, and with faststart a browser begins
# playing before the whole file has arrived. Re-encoded in place, before the size caps, so the
# caps measure what is actually uploaded. No ffmpeg: the file goes as it is, with a warning.
for r in "${refs[@]}"; do
  case $r in *.mp4) ;; *) continue ;; esac
  f="$dir/$r"
  if ! command -v ffmpeg >/dev/null || ! command -v ffprobe >/dev/null; then
    echo "warning: no ffmpeg here; ./$r goes up as recorded and may not play in Safari" >&2
    continue
  fi
  codec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$f" || true)
  [ "$codec" = h264 ] && continue
  ffmpeg -nostdin -y -loglevel error -i "$f" -c:v libx264 -preset veryfast -crf 28 \
    -pix_fmt yuv420p -movflags +faststart -an "$f.h264.mp4" ||
    die "./$r is ${codec:-unreadable} and could not be re-encoded to H.264"
  mv "$f.h264.mp4" "$f"
  echo "re-encoded ./$r from ${codec:-?} to h264" >&2
done

total=0
for r in "${refs[@]}"; do
  f="$dir/$r"
  size=$(stat -c %s "$f")
  [ "$size" -le "$max_file" ] || die "./$r is $size bytes; the cap is $max_file"
  total=$((total + size))
done
[ "$total" -le "$max_total" ] || die "$total bytes in all; the cap is $max_total"

r2_vars=(QA_R2_ENDPOINT QA_R2_BUCKET QA_R2_PUBLIC_URL QA_R2_ACCESS_KEY_ID QA_R2_SECRET_ACCESS_KEY)
set_count=0
for v in "${r2_vars[@]}"; do [ -n "${!v:-}" ] && set_count=$((set_count + 1)); done
if [ "$set_count" = "${#r2_vars[@]}" ]; then
  backend=r2
elif [ "$set_count" = 0 ]; then
  backend=gh
else
  missing=(); for v in "${r2_vars[@]}"; do [ -n "${!v:-}" ] || missing+=("$v"); done
  die "R2 is half configured; unset: ${missing[*]}"
fi
[ "$backend" = gh ] && [ -z "$pr" ] && die "--upload-only needs the QA_R2_* variables; none are set"

# Resolved once, from the checkout, so gh run from inside $dir still knows the repository.
export GH_REPO=${GH_REPO:-$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)}
[ -n "$GH_REPO" ] || die "run this from the repository checkout, or set GH_REPO=<owner>/<repo>"

if [ -n "$pr" ]; then
  head=$(gh pr view "$pr" --json headRefOid -q .headRefOid)
  if [ "$head" != "$(git rev-parse HEAD 2>/dev/null || true)" ]; then
    echo "warning: PR head is ${head:0:12} but this checkout is at $(git rev-parse --short=12 HEAD 2>/dev/null || echo '?')" >&2
    echo "         — push first, or the evidence describes a commit the PR does not have." >&2
  fi
fi

out="$dir/block.published.md"
cp "$block" "$out"

if [ "$backend" = r2 ]; then
  repo=$GH_REPO
  prefix="${repo##*/}/${pr:-adhoc}/$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  endpoint=${QA_R2_ENDPOINT%/}
  public=${QA_R2_PUBLIC_URL%/}
  for r in "${refs[@]}"; do
    f="$dir/$r"
    key="$prefix/$r"
    case $r in *.png) type=image/png ;; *.mp4) type=video/mp4 ;; esac
    # The payload hash goes in as a header so curl signs it rather than UNSIGNED-PAYLOAD.
    sum=$(sha256sum "$f" | cut -d' ' -f1)
    code=$(curl -sS -o "$dir/.r2-response" -w '%{http_code}' -X PUT \
      --aws-sigv4 'aws:amz:auto:s3' --user "$QA_R2_ACCESS_KEY_ID:$QA_R2_SECRET_ACCESS_KEY" \
      -H "x-amz-content-sha256: $sum" -H "content-type: $type" \
      -H 'cache-control: public, max-age=31536000, immutable' \
      --data-binary "@$f" "$endpoint/$QA_R2_BUCKET/$key") || die "upload of ./$r failed: curl exited non-zero"
    [ "$code" = 200 ] || die "upload of ./$r refused with HTTP $code: $(head -c 500 "$dir/.r2-response")"
    # What a reviewer's browser will fetch, not what the API said: a bucket whose public
    # domain is not connected uploads fine and renders nothing.
    pcode=$(curl -sS -o /dev/null -w '%{http_code}' -I "$public/$key") || pcode=000
    [ "$pcode" = 200 ] || die "./$r uploaded but $public/$key answers HTTP $pcode — is the public domain connected to $QA_R2_BUCKET?"
    R="$r" U="$public/$key" perl -pi -e 's/(?<![\w\/.])\.\/\Q$ENV{R}\E(?![\w.-])/$ENV{U}/g' "$out"
    echo "uploaded ./$r → $public/$key" >&2
  done
  rm -f "$dir/.r2-response"
  if grep -qE '(^|[^A-Za-z0-9/.])\./[A-Za-z0-9._-]+\.(png|mp4)' "$out"; then
    die "a ./ reference survived the rewrite in $out"
  fi
fi

if [ -z "$pr" ]; then
  cat "$out"
  exit 0
fi

# Splice: drop any previous block, keep everything the author wrote around it.
gh pr view "$pr" --json body -q .body >"$dir/body.old"
START="$start" END="$end" perl -0777 -pe 's/\Q$ENV{START}\E.*?\Q$ENV{END}\E//gs; s/\s+\z//' \
  "$dir/body.old" >"$dir/body.md"
[ -s "$dir/body.md" ] && printf '\n\n' >>"$dir/body.md"
cat "$out" >>"$dir/body.md"
rm -f "$dir/body.old"

if [ "$backend" = r2 ]; then
  gh pr edit "$pr" --body-file "$dir/body.md" >/dev/null || die "gh pr edit failed; read the PR body"
else
  gh pr edit --help 2>/dev/null | grep -q -- '--attach' || die "this gh has no --attach (need 2.104+)"
  attach=(); for r in "${refs[@]}"; do attach+=(--attach "./$r"); done
  # From inside the directory, so --attach paths and the body's ./ references are the same string.
  (cd "$dir" && gh pr edit "$pr" --body-file body.md "${attach[@]}" >/dev/null) ||
    die "gh pr edit exited non-zero — some uploads may have failed; read the PR body"
fi

after=$(gh pr view "$pr" --json body -q .body)
n=$(grep -cF "$start" <<<"$after" || true)
[ "$n" = 1 ] || die "expected one validation block in the PR body, found $n"
if grep -qE '\]\(\./' <<<"$after"; then
  die "the PR body still has ./ references — broken images: $(grep -oE '\]\(\./[^)]*\)' <<<"$after" | tr '\n' ' ')"
fi
echo "PR #$pr updated via $backend with ${#refs[@]} files: $(gh pr view "$pr" --json url -q .url)"
