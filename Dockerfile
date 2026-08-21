FROM golang:1.26.2-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /limiance-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /limiance-api /limiance-api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/limiance-api"]
