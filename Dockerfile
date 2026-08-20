FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/atlas-bridge ./cmd/atlas-bridge

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/atlas-bridge /usr/local/bin/atlas-bridge
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/atlas-bridge"]

