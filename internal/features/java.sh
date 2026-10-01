#!/usr/bin/env bash
# Feature: java — a Temurin JDK on the scratch volume, plus `jdk` to list,
# switch and remove JDKs and build caches. 17 by default, MEGH_JAVA_VERSION=21
# for 21. Gradle and Maven caches sit beside it, one set per box. A rebuilt box
# finds the JDK already there and only re-checks it. Idempotent.
set -uo pipefail
log() { echo "[megh-enable] $*"; }

major="${MEGH_JAVA_VERSION:-17}"

case "$(uname -m)" in
  x86_64)        arch=x64 ;;
  aarch64|arm64) arch=aarch64 ;;
  *) log "no Temurin build pinned for $(uname -m)"; exit 1 ;;
esac

# One pin per major and arch. To update, change build and sha256 together
# (both from api.adoptium.net/v3/assets/latest/<major>/hotspot); the next
# `megh enable java` installs the new build, moves the symlink and deletes the
# old one, so updating never leaves a second copy behind.
case "${major}:${arch}" in
  17:x64)     build="17.0.20.1+1"; sha256="3808d1d15e3ec6bd5b84057fb5d84c33d8a1536a258146bcea2e603fc726e08e" ;;
  17:aarch64) build="17.0.20.1+1"; sha256="457b57af8f9c93ec39080bb8c764f559dc8c89a6da1a39d718a400b7890d3e41" ;;
  21:x64)     build="21.0.12.1+1"; sha256="ce79869e1307ed8ee1e2baa86a412b1eb5b75d10a01006d788a6f968bcfaee94" ;;
  21:aarch64) build="21.0.12.1+1"; sha256="23e37e026f12f3e706f18938ff611db3032d075b09d0879a25d06718c773e223" ;;
  *) log "no Temurin ${major} pinned (pinned: 17, 21)"; exit 1 ;;
esac

# The JDK is ~300 MB unpacked. On the volume a rebuilt box keeps it; on the
# container disk every box downloads it again. Read-only once installed, so
# boxes sharing the volume can share it too.
if [ -d /mnt/work ] && [ -w /mnt/work ]; then
  root="/mnt/work/cache/${ARCH_TAG:-$(uname -m)}/java"
else
  root="/opt/megh/java"
  log "no writable /mnt/work; the JDK goes to the local disk and is lost on rebuild"
fi
# Build caches are written by every build, and two boxes writing one Gradle or
# Maven cache over a shared mount is how those caches get corrupted (the same
# reason postgres keeps its data off the volume). So each box gets its own,
# named by host so a rebuilt box with the same name picks its cache back up.
box="$(hostname -s 2>/dev/null || hostname)"
caches="${root}/caches/${box}"
mkdir -p "${root}/jdk" "${caches}/gradle" "${caches}/m2" || { log "cannot create ${root}"; exit 1; }

dir="${root}/jdk/temurin-${build}"
version="${build%%+*}"

installed() { "${dir}/bin/javac" -version 2>&1 | grep -qx "javac ${version}"; }

if installed; then
  log "Temurin ${build} already on ${dir}"
else
  url="https://github.com/adoptium/temurin${major}-binaries/releases/download/jdk-${build/+/%2B}/OpenJDK${major}U-jdk_${arch}_linux_hotspot_${build/+/_}.tar.gz"
  # Unpack beside the target and rename into place, so an interrupted run
  # leaves a .tmp directory for `jdk prune` rather than half a JDK that the
  # check above would trust.
  tmp="$(mktemp -d "${root}/jdk/.tmp-XXXXXX")" || { log "mktemp failed"; exit 1; }
  log "downloading Temurin ${build} (${arch}, ~190 MB)"
  if ! curl -fsSL -o "${tmp}/jdk.tar.gz" "${url}"; then
    log "download failed: ${url}"; rm -rf "${tmp}"; exit 1
  fi
  if ! echo "${sha256}  ${tmp}/jdk.tar.gz" | sha256sum -c --status; then
    log "checksum mismatch for ${url}; not installing it"; rm -rf "${tmp}"; exit 1
  fi
  mkdir "${tmp}/jdk"
  if ! tar -xzf "${tmp}/jdk.tar.gz" -C "${tmp}/jdk" --strip-components=1; then
    log "unpacking failed"; rm -rf "${tmp}"; exit 1
  fi
  rm -rf "${dir}"
  mv "${tmp}/jdk" "${dir}" && rm -rf "${tmp}"
  installed || { log "unpacked, but ${dir}/bin/javac does not report ${version}"; exit 1; }
fi

# <major> names the build in use. JAVA_HOME goes through `default`, which names
# a major, so switching or updating is a symlink change and nothing else.
ln -sfn "temurin-${build}" "${root}/jdk/${major}"
[ -L "${root}/jdk/default" ] || ln -sfn "${major}" "${root}/jdk/default"

# An update leaves the previous build of this major behind; remove it here
# rather than waiting for someone to notice the volume filling up.
for old in "${root}/jdk/temurin-${major}."*; do
  [ -d "${old}" ] && [ "${old}" != "${dir}" ] || continue
  log "removing the replaced build $(basename "${old}")"
  rm -rf "${old}"
done

log "installing the jdk helper (/usr/local/bin/jdk)"
{
  echo '#!/usr/bin/env bash'
  echo "# megh: written by 'megh enable java'. Lists, switches and removes the JDKs"
  echo "# and build caches it installed, so they do not accumulate."
  echo "JAVA_ROOT=\"${root}\""
  echo "BOX=\"${box}\""
} > /usr/local/bin/jdk
cat >> /usr/local/bin/jdk <<'JDK'
set -uo pipefail
jdks="${JAVA_ROOT}/jdk"
caches="${JAVA_ROOT}/caches"

usage() {
  cat <<'USAGE'
usage: jdk <command>
  ls                 installed JDKs, which major and default use them, and cache sizes
  use <major>        make <major> the default (new login shells pick it up)
  rm <major>         remove a major and its JDK
  prune              remove JDKs no major uses, dangling links, interrupted installs
  clean-caches [box|--all]
                     remove a box's Gradle and Maven caches (this box by default)
Install or update a major with: MEGH_JAVA_VERSION=<major> megh enable java
USAGE
}

size() { du -sh "$1" 2>/dev/null | cut -f1; }

cmd_ls() {
  local def=""
  [ -L "${jdks}/default" ] && def="$(readlink "${jdks}/default")"
  echo "JDKs in ${jdks}:"
  local found=0 d name majors l
  for d in "${jdks}"/temurin-*; do
    [ -d "$d" ] || continue
    found=1
    name="$(basename "$d")"
    majors=""
    for l in "${jdks}"/*; do
      [ -L "$l" ] && [ "$(basename "$l")" != default ] && [ "$(readlink "$l")" = "$name" ] || continue
      majors="${majors} $(basename "$l")"
      [ "$(basename "$l")" = "$def" ] && majors="${majors}(default)"
    done
    printf '  %-26s %6s  major:%s\n' "$name" "$(size "$d")" "${majors:- none, prune removes it}"
  done
  [ "$found" = 1 ] || echo "  none"
  # Left by an interrupted install, or one still running on another box that
  # shares the volume. The installer leaves them alone for that reason.
  for d in "${jdks}"/.tmp-*; do
    [ -d "$d" ] && printf '  %-26s %6s  interrupted install, prune removes it\n' "$(basename "$d")" "$(size "$d")"
  done
  echo "Build caches in ${caches}:"
  found=0
  for d in "${caches}"/*; do
    [ -d "$d" ] || continue
    found=1
    name="$(basename "$d")"
    printf '  %-26s %6s%s\n' "$name" "$(size "$d")" "$([ "$name" = "$BOX" ] && echo '  (this box)')"
  done
  [ "$found" = 1 ] || echo "  none"
}

cmd_use() {
  local m="${1:?usage: jdk use <major>}"
  [ -L "${jdks}/${m}" ] && [ -d "${jdks}/${m}/" ] || { echo "jdk: ${m} is not installed (MEGH_JAVA_VERSION=${m} megh enable java)" >&2; exit 1; }
  ln -sfn "$m" "${jdks}/default"
  echo "jdk: default is now ${m} ($(readlink "${jdks}/${m}")); open a new login shell"
}

cmd_rm() {
  local m="${1:?usage: jdk rm <major>}"
  [ -L "${jdks}/${m}" ] || { echo "jdk: no major ${m}" >&2; exit 1; }
  local target="${jdks}/$(readlink "${jdks}/${m}")"
  rm -f "${jdks}/${m}"
  # Another major can name the same build only by hand, but check anyway.
  local l still=0
  for l in "${jdks}"/*; do
    [ -L "$l" ] && [ "${jdks}/$(readlink "$l")" = "$target" ] && still=1
  done
  [ "$still" = 1 ] || rm -rf "$target"
  if [ "$(readlink "${jdks}/default" 2>/dev/null)" = "$m" ]; then
    rm -f "${jdks}/default"
    echo "jdk: removed ${m}, which was the default; pick another with 'jdk use <major>'"
  else
    echo "jdk: removed ${m}"
  fi
}

cmd_prune() {
  local l d name used
  for l in "${jdks}"/*; do
    [ -L "$l" ] && [ ! -e "$l" ] && { echo "jdk: removing dangling $(basename "$l")"; rm -f "$l"; }
  done
  for d in "${jdks}"/.tmp-*; do
    [ -d "$d" ] && { echo "jdk: removing interrupted install $(basename "$d")"; rm -rf "$d"; }
  done
  for d in "${jdks}"/temurin-*; do
    [ -d "$d" ] || continue
    name="$(basename "$d")"
    used=0
    for l in "${jdks}"/*; do
      [ -L "$l" ] && [ "$(readlink "$l")" = "$name" ] && used=1
    done
    [ "$used" = 1 ] || { echo "jdk: removing unused ${name} ($(size "$d"))"; rm -rf "$d"; }
  done
}

cmd_clean_caches() {
  local which="${1:-$BOX}"
  if [ "$which" = "--all" ]; then
    echo "jdk: removing every box's build caches ($(size "$caches"))"
    rm -rf "${caches:?}"/*
  else
    [ -d "${caches}/${which}" ] || { echo "jdk: no caches for ${which}" >&2; exit 1; }
    echo "jdk: removing ${which}'s build caches ($(size "${caches}/${which}"))"
    rm -rf "${caches:?}/${which}"
  fi
}

case "${1:-ls}" in
  ls)           cmd_ls ;;
  use)          shift; cmd_use "$@" ;;
  rm)           shift; cmd_rm "$@" ;;
  prune)        cmd_prune ;;
  clean-caches) shift; cmd_clean_caches "$@" ;;
  -h|--help|help) usage ;;
  *)            usage >&2; exit 2 ;;
esac
JDK
chmod 0755 /usr/local/bin/jdk

# JAVA_HOME follows `default`, so `jdk use` takes effect in the next login
# shell without rewriting this file. The cache paths are this box's own.
{
  echo "# megh: written by 'megh enable java'. 'jdk ls' shows what is installed."
  echo "export JAVA_HOME=\"${root}/jdk/default\""
  echo "export PATH=\"\${JAVA_HOME}/bin:\${PATH}\""
  echo "export GRADLE_USER_HOME=\"${caches}/gradle\""
  echo "export MAVEN_OPTS=\"-Dmaven.repo.local=${caches}/m2\${MAVEN_OPTS:+ \$MAVEN_OPTS}\""
} > /etc/profile.d/megh-java.sh
chmod 0644 /etc/profile.d/megh-java.sh

# Verify by compiling and running something, rather than trusting exit codes:
# a JDK that unpacked but cannot compile would otherwise print "ready".
check="$(mktemp -d)"
cat > "${check}/Check.java" <<'JAVA'
public class Check {
  public static void main(String[] args) {
    System.out.println("ok " + System.getProperty("java.version"));
  }
}
JAVA
if ! "${dir}/bin/javac" -d "${check}" "${check}/Check.java" >/dev/null 2>&1; then
  log "javac from ${dir} could not compile a one-class program"; rm -rf "${check}"; exit 1
fi
out="$("${dir}/bin/java" -cp "${check}" Check 2>&1)"
rm -rf "${check}"
if [ "${out}" != "ok ${version}" ]; then
  log "compiled, but running it printed '${out}', not 'ok ${version}'"; exit 1
fi

log "Temurin ${build} ready on ${dir} ($(readlink "${root}/jdk/default") is the default)"
log "open a new login shell for JAVA_HOME, or: source /etc/profile.d/megh-java.sh"
log "'jdk ls' shows JDKs and cache sizes; 'jdk rm', 'jdk prune' and 'jdk clean-caches' remove them"
