# Сборка: статика клиента вшивается в бинарник, образ содержит только его.
FROM golang:1.27-alpine AS build
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.buildVersion=${BUILD_VERSION}" \
    -o /out/snowbrawl-server ./cmd/snowbrawl-server

# Каталог для файла ролей и банов создаём в образе с владельцем nonroot: иначе docker создаст
# точку монтирования от root, и сервер (UID 65532) не сможет туда писать — баны молча пропадут.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/snowbrawl-server /snowbrawl-server
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/snowbrawl-server"]
