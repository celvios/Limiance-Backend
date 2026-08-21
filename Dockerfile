FROM golang:1.26.2-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN for command in api migrate outbox event-router deposits notifications deposit-reconcile-sepolia; do \
      CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o "/limiance/${command}" "./cmd/${command}"; \
    done

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /limiance/ /app/
COPY --from=build /src/migrations /app/migrations
EXPOSE 8080
USER nonroot:nonroot
CMD ["/app/api"]
