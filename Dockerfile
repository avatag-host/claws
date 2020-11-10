# ----------------------------------
# Pterodactyl Panel Dockerfile
# ----------------------------------

FROM golang:1.15-alpine
COPY . /go/claws/
WORKDIR /go/claws/
RUN apk add --no-cache upx \
 && go build -ldflags="-s -w" \
 && upx --brute wings

FROM alpine:latest
COPY --from=0 /go/wings/wings /usr/bin/
CMD ["wings","--config", "/etc/claws/config.yml"]