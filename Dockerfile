# The image wraps the binaries scripts/package.sh already built (same bits as the
# release archives); it does not compile anything.
#
#   sh scripts/package.sh v0.1.0
#   docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/vantt/multica-intake:v0.1.0 .
#
# Configure with MULTICA_INTAKE_* env vars (see `multica-intake config explain`), or
# mount a config.yaml at /etc/multica-intake/config.yaml.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/vantt/multica-intake" \
      org.opencontainers.image.description="MCP server that lets a chat bot hand work to a Multica team, as suggestions only" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --chmod=0755 dist/bin/${TARGETOS}-${TARGETARCH}/multica-intake /usr/local/bin/multica-intake
ENV HOME=/tmp \
    MULTICA_INTAKE_CONFIG_DIR=/etc/multica-intake \
    MULTICA_INTAKE_STATE_DIR=/tmp/multica-intake
# A fixed uid outside the useradd range, so it never matches a host account
# (fgOPC convention: 611xx block of the MausBot stack).
USER 61103:61103
EXPOSE 8801
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/usr/local/bin/multica-intake", "internal-healthcheck"]
ENTRYPOINT ["/usr/local/bin/multica-intake"]
CMD ["mcp", "--http"]
