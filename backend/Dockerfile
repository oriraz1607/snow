FROM docker.io/library/golang:1.26-alpine AS builder

WORKDIR /src

COPY backend/go.mod backend/go.sum ./backend/
WORKDIR /src/backend
RUN go mod download

COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/mocksnow ./cmd/mocksnow

FROM docker.io/library/alpine:3.21

RUN apk add --no-cache ca-certificates wget tzdata

COPY --from=builder /out/api /bin/api
COPY --from=builder /out/migrate /bin/migrate
COPY --from=builder /out/mocksnow /bin/mocksnow
COPY migrations /migrations

WORKDIR /
ENTRYPOINT ["/bin/api"]
