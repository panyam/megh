# The gateway image (`megh gw up`, DESIGN.md "Roles"): tailscale plus the megh
# binary, and nothing from the dev image. The container holds a tailnet
# identity, so the less else is in it the better.
#
# The official tailscale image is Alpine with tailscaled, the CLI and CA
# certificates, published for amd64 and arm64. Everything megh runs inside is
# plain sh (busybox), which TestGatewayRunsUnderPlainSh pins.
#
# The build context holds megh-amd64 and megh-arm64 (static, CGO_ENABLED=0),
# staged by CI or `make image-local-gw`; TARGETARCH picks one.
FROM tailscale/tailscale:v1.102.5

ARG TARGETARCH
COPY megh-${TARGETARCH} /usr/local/bin/megh

# `megh gw up` passes its own sh entrypoint and script; this default only
# answers someone running the image by hand.
ENTRYPOINT ["megh", "gw"]
CMD ["--help"]
