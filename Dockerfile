FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mailcatcher ./cmd/mailcatcher

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mailcatcher /mailcatcher
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/mailcatcher"]
