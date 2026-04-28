# syntax=docker/dockerfile:1.7

# Build the mock IDR bank from the Selatpay module so the binary
# stays in lockstep with the rail client. Multi-stage so the runtime
# image carries only the static binary.
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/mockidrbank ./cmd/mockidrbank

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/mockidrbank /mockidrbank
EXPOSE 9100
ENTRYPOINT ["/mockidrbank"]
