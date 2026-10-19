FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/weather-sum .

FROM golang:1.26.9-alpine AS goose-build
RUN CGO_ENABLED=0 go install -tags='no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb' github.com/pressly/goose/v3/cmd/goose@v3.27.2

FROM alpine:3.24 AS base
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
USER 65532:65532

FROM base AS migrator
COPY --from=goose-build /go/bin/goose /usr/local/bin/goose
COPY migrations /app/migrations
ENTRYPOINT ["goose", "-env", "none", "-dir", "/app/migrations"]
CMD ["up"]

FROM build AS fakeapi-build
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /out/fakeapi ./tests/integration/fakeapi

FROM base AS test-api
COPY --from=fakeapi-build /out/fakeapi /usr/local/bin/fakeapi
ENTRYPOINT ["fakeapi"]

FROM base AS runtime
COPY --from=build /out/weather-sum /usr/local/bin/weather-sum
ENTRYPOINT ["weather-sum"]
