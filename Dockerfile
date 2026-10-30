# SPDX-License-Identifier: GPL-3.0-only
FROM debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4 AS build
RUN apt-get update && apt-get install -y --no-install-recommends golang-go build-essential cmake ninja-build curl ca-certificates unzip git perl pkg-config libopus-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY native/build.sh native/fetch.sh ./native/
RUN sh native/build.sh /opt/quasard /var/tmp/quasard-native && rm -rf /var/tmp/quasard-native
COPY native/build-audio.sh ./native/
RUN sh native/build-audio.sh /opt/quasard /var/tmp/quasard-audio && rm -rf /var/tmp/quasard-audio
ENV CGO_ENABLED=1 LD_LIBRARY_PATH=/opt/quasard/lib
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN flags="-s -w -X github.com/maintainer64/quasard/internal/buildinfo.Version=$VERSION -X github.com/maintainer64/quasard/internal/buildinfo.Commit=$COMMIT -X github.com/maintainer64/quasard/internal/buildinfo.Date=$BUILD_DATE"; go build -tags native -trimpath -ldflags="$flags" -o /out/quasard ./cmd/quasard && go build -tags native -trimpath -ldflags="$flags" -o /out/quasard-tools ./cmd/quasard-tools
COPY native/runtime-files.sh ./native/
COPY scripts/go-notices.sh ./scripts/
RUN sh scripts/go-notices.sh /opt/quasard/share/licenses
RUN sh native/runtime-files.sh /runtime && mkdir -p /runtime/app/data /runtime/app/models /runtime/app/certs && chown -R 10001:10001 /runtime/app

FROM build AS bundle-build
COPY scripts/bundle-launch.sh /bundle/launch
COPY config.example.toml LICENSE THIRD_PARTY_NOTICES.md /bundle/
COPY alice_server/aliceapi.desc /bundle/alice_server/aliceapi.desc
COPY alice_server/assets /bundle/alice_server/assets
RUN mkdir -p /bundle/runtime/usr/local/bin /bundle/bin /bundle/data /bundle/models /bundle/certs && cp -a /runtime/. /bundle/runtime/ && cp /out/* /bundle/runtime/usr/local/bin/ && chmod 755 /bundle/launch && ln -s launch /bundle/quasard && ln -s launch /bundle/quasard-tools && ln -s ../launch /bundle/bin/ffmpeg && printf 'Copy config.example.toml to config.toml; run ./quasard --config config.toml. Requires Linux/glibc kernel environment; native loader/libraries included. Models are NOT included. Windows server: Docker/WSL2 only. See https://github.com/maintainer64/quasard/tree/%s/docs\n' "$COMMIT" > /bundle/README.txt

FROM scratch AS bundle
COPY --from=bundle-build /bundle/ /

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:3ee00291cf386ec52a552e6a858d555debd74776eb4bfa53fa3df2cd6677515e AS runtime
WORKDIR /app
COPY --from=build /runtime/ /
COPY --from=build /out/ /usr/local/bin/
COPY alice_server/aliceapi.desc /app/alice_server/aliceapi.desc
COPY alice_server/assets /app/alice_server/assets
COPY LICENSE THIRD_PARTY_NOTICES.md /app/
COPY config.example.toml /app/config.toml
ENV LD_LIBRARY_PATH=/opt/quasard/lib OMP_NUM_THREADS=1 QUASARD_ESPEAK_DATA=/opt/quasard/share/espeak-ng-data
USER 10001:10001
EXPOSE 8080 8443
HEALTHCHECK --interval=30s --start-period=120s --timeout=5s CMD ["/usr/local/bin/quasard", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/quasard"]
CMD ["--config", "/app/config.toml"]
