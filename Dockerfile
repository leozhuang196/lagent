# One image for every role: `lagent serve --roles=...`
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/ ./
RUN if [ -f package.json ]; then npm ci && npm run build; else mkdir -p ../internal/transport/static/dist; fi

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/transport/static/dist ./internal/transport/static/dist
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/lagent ./cmd/lagent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata bash coreutils \
 && adduser -D -u 10001 lagent && adduser -D -u 10002 sandbox
COPY --from=build /out/lagent /usr/local/bin/lagent
USER lagent
WORKDIR /home/lagent
EXPOSE 8080
ENTRYPOINT ["lagent"]
CMD ["serve", "--config", "/etc/lagent/lagent.yaml"]
