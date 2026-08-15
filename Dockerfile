# TamizChat backend runtime image.
#
# The binary is cross-compiled on the host (`make docker-bin`) rather than inside
# the image. The project is CGo-free by design, so `GOOS=linux CGO_ENABLED=0 go
# build` from any machine produces the same static binary — and doing it outside
# the image means the image build needs no access to the Go module proxy at all,
# which is what makes this work behind a restrictive proxy or offline.
#
# Alpine rather than scratch, because the image also has to be able to run the
# interactive admin panel and the compose healthcheck's wget.

FROM alpine:3.22

RUN adduser -D -u 10001 tamizchat \
    && mkdir -p /data \
    && chown tamizchat:tamizchat /data

ARG BIN=dist/tamizchat-linux-amd64
COPY ${BIN} /usr/local/bin/tamizchat

USER tamizchat
WORKDIR /data

# The database, uploads and backups all live under this one directory.
ENV TAMIZCHAT_DB=/data/tamizchat.db
VOLUME /data
EXPOSE 8080

ENTRYPOINT ["tamizchat"]
CMD ["run"]
