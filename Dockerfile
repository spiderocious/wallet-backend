# Two stages: build a static binary, ship it on a minimal image.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/ /app/
COPY entrypoint.sh /app/entrypoint.sh
# The upload directory must exist and belong to the unprivileged user.
RUN mkdir -p /app/data && chown -R app:app /app/data
USER app
ENV PORT=8080 DATA_DIR=/app/data
EXPOSE 8080
# Migrate, seed, serve. Railway and plain `docker run` both go through this.
CMD ["/app/entrypoint.sh"]
