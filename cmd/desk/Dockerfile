FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/desk/ cmd/desk/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /desk ./cmd/desk

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /desk /usr/local/bin/desk
# The desk runs as root: it drives the Docker socket (spec section 5.3).
EXPOSE 8110
ENTRYPOINT ["desk"]
