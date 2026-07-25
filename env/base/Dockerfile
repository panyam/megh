# megh base dev environment (CONTAINER artifact).
#
# One of two artifacts built from the SAME source of truth (provision.sh):
#   - this Dockerfile -> container image, for container-native providers (RunPod)
#   - env/base/packer -> VM image,       for VM-native providers (Hetzner, EC2/AMI) [later]
#
# The container runs the dev env as a sandboxed process tree. On VM-native
# providers we do NOT run this container inside a VM; we run the dev env
# directly on the VM from the VM image, so the box has a real, native Docker
# daemon for dev workflows. Hence INSTALL_DOCKER=0 here: a container does not
# need a daemon inside it.
#
# RunPod CPU pods are x86_64, so this targets linux/amd64. A second arch is a CI
# matrix entry, not an edit here.

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISPLAY=:99 \
    PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

ARG GO_VERSION=1.26.4
ARG TTYD_VERSION=1.7.7
ARG MEGH_SLIM=0

COPY provision.sh /tmp/provision.sh
RUN INSTALL_DOCKER=0 TARGET_ARCH=amd64 \
      GO_VERSION="${GO_VERSION}" TTYD_VERSION="${TTYD_VERSION}" MEGH_SLIM="${MEGH_SLIM}" \
      bash /tmp/provision.sh \
    && rm /tmp/provision.sh

# Build stamp so you can verify from the box which image version is running:
#   cat /etc/megh/build-info   (ref = the git SHA CI built from)
ARG MEGH_BUILD_REF=dev
RUN mkdir -p /etc/megh && \
    printf 'ref=%s\nbuilt=%s\n' "${MEGH_BUILD_REF}" "$(date -u +%FT%TZ)" > /etc/megh/build-info

EXPOSE 22 7681 6080

# megh itself, so a box can manage/hydrate itself locally (no jump box). The
# binary + config are staged into the build context by CI (see build-env.yml).
# The megh REPO lands via the repos: list on hydrate. No RUNPOD_API_KEY is baked;
# add it on the box only to elevate megh to manage OTHER boxes.
COPY megh /usr/local/bin/megh
COPY megh.yaml /etc/megh/megh.yaml
RUN chmod +x /usr/local/bin/megh

COPY entrypoint.sh /usr/local/bin/megh-entrypoint
COPY flush-sessions.sh /usr/local/bin/megh-flush-sessions
COPY enable-vnc.sh /usr/local/bin/megh-enable-vnc
RUN chmod +x /usr/local/bin/megh-entrypoint /usr/local/bin/megh-flush-sessions /usr/local/bin/megh-enable-vnc

ENTRYPOINT ["/usr/local/bin/megh-entrypoint"]
