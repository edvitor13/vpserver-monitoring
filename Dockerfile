# syntax=docker/dockerfile:1
# Imagem do vpserver-monitoring (linux/arm64 e linux/amd64). O Go compila
# cruzado na máquina do build (rápido, sem emulação); a imagem final é a
# distroless "static" (~2 MB + o binário), roda como usuário sem privilégio.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/vpmon ./cmd/vpmon

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/edvitor13/vpserver-monitoring" \
      org.opencontainers.image.title="vpserver-monitoring" \
      org.opencontainers.image.description="Painel leve de monitoramento de servidor (feito para Oracle Cloud Always Free): consumo por app, banda, disco, logs, alertas e chat com IA."
COPY --from=build /out/vpmon /vpmon
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/vpmon"]
