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

RUN apk add --no-cache ca-certificates wget tzdata \
 && addgroup -S -g 10001 appgroup \
 && adduser -S -D -H -u 10001 -G appgroup appuser

COPY --from=builder /out/api /bin/api
COPY --from=builder /out/migrate /bin/migrate
COPY --from=builder /out/mocksnow /bin/mocksnow
COPY migrations /migrations

RUN chmod 755 /bin/api /bin/migrate /bin/mocksnow \
 && chmod -R a+rX /migrations

USER 10001:10001
WORKDIR /
ENTRYPOINT ["/bin/api"]
