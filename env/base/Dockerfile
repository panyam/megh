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

ARG GO_VERSION=1.22.5
ARG TTYD_VERSION=1.7.7

COPY provision.sh /tmp/provision.sh
RUN INSTALL_DOCKER=0 TARGET_ARCH=amd64 \
      GO_VERSION="${GO_VERSION}" TTYD_VERSION="${TTYD_VERSION}" \
      bash /tmp/provision.sh \
    && rm /tmp/provision.sh

EXPOSE 22 7681 6080

COPY entrypoint.sh /usr/local/bin/megh-entrypoint
RUN chmod +x /usr/local/bin/megh-entrypoint

ENTRYPOINT ["/usr/local/bin/megh-entrypoint"]
