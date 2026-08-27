FROM golang:1.26.2-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN for command in api migrate outbox event-router deposits withdrawals notifications deposit-reconcile-sepolia bootstrap-platform-admin enable-staging-conversions fee-refresh; do \
        CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o "/limiance/${command}" "./cmd/${command}"; \
    done

# A static Go binary needs only the system CA bundle for its HTTPS provider
# clients. Using scratch removes the gcr.io runtime dependency and retains a
# non-root, shell-free production image.
FROM scratch
WORKDIR /app
COPY --from=build /limiance/ /app/
COPY --from=build /src/migrations /app/migrations
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
EXPOSE 8080
USER 65532:65532
CMD ["/app/api"]
