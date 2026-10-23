FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X github.com/xudipta/edge-mcp/internal/server.Version=${VERSION}" -o /edge-mcp ./cmd/edge-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /edge-mcp /edge-mcp
USER nonroot:nonroot
ENTRYPOINT ["/edge-mcp"]
