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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/snowbrawl-server /snowbrawl-server
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/snowbrawl-server"]
