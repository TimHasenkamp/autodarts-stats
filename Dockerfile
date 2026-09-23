# --- Frontend ---
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN mkdir -p /src/server/internal/web/dist && npm run build

# --- Backend ---
FROM golang:1.27-alpine AS build
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/server/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /autodarts-stats ./cmd/autodarts-stats

# --- Runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /data
COPY --from=build /autodarts-stats /autodarts-stats
ENV PORT=8080 DB_PATH=/data/autodarts-stats.db
EXPOSE 8080
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/autodarts-stats"]
