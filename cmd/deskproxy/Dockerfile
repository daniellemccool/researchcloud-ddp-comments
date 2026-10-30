FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/deskproxy/ cmd/deskproxy/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /deskproxy ./cmd/deskproxy

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /deskproxy /usr/local/bin/deskproxy
# Captures and the CA directory are owned by uid 1000 on their volumes; the
# init script creates them so.
USER 1000:1000
EXPOSE 8080 8081
ENTRYPOINT ["deskproxy"]
